package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/portal"
)

const portalNotificationWorkerLock int64 = 83003

func (s *Server) handleInternalPortalNotifications() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()

		lockConnection, err := s.DB.Acquire(ctx)
		if err != nil {
			http.Error(w, "portal notification worker lock is unavailable", http.StatusInternalServerError)
			return
		}
		defer lockConnection.Release()
		var locked bool
		if err := lockConnection.QueryRow(
			ctx,
			`SELECT pg_try_advisory_lock($1)`,
			portalNotificationWorkerLock,
		).Scan(&locked); err != nil {
			http.Error(w, "portal notification worker lock is unavailable", http.StatusInternalServerError)
			return
		}
		if !locked {
			http.Error(w, "portal notification worker is already running", http.StatusConflict)
			return
		}
		defer func() {
			unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer unlockCancel()
			_, _ = lockConnection.Exec(
				unlockCtx,
				`SELECT pg_advisory_unlock($1)`,
				portalNotificationWorkerLock,
			)
		}()

		materialized, err := s.PortalStore.MaterializeSecurityNotifications(ctx, 100)
		if err != nil {
			http.Error(w, "portal security events could not be materialized", http.StatusInternalServerError)
			return
		}

		mailer := email.NewFromEnv()
		if !mailer.SensitiveDeliveryConfigured() {
			writePortalNotificationWorkerResponse(
				w,
				http.StatusServiceUnavailable,
				materialized,
				0,
				0,
				"SMTP is not configured; notifications remain queued",
			)
			return
		}

		sent := 0
		failed := 0
		for selected := 0; selected < 50; selected++ {
			notification, err := s.PortalStore.ClaimSecurityNotification(ctx)
			if err != nil {
				http.Error(w, "portal notification queue could not be claimed", http.StatusInternalServerError)
				return
			}
			if notification == nil {
				break
			}

			subject, body, err := renderPortalSecurityNotification(
				*notification,
				publicBaseURL(r),
			)
			if err == nil {
				err = mailer.SendSensitive(notification.Recipient, subject, body)
			}
			if err != nil {
				failed++
				if markErr := s.PortalStore.MarkSecurityNotificationFailed(
					ctx,
					notification.ID,
					notification.AttemptCount,
					err.Error(),
				); markErr != nil {
					http.Error(w, "portal notification failure could not be recorded", http.StatusInternalServerError)
					return
				}
				continue
			}
			if err := s.PortalStore.MarkSecurityNotificationSent(
				ctx,
				notification.ID,
				notification.AttemptCount,
			); err != nil {
				http.Error(w, "portal notification success could not be recorded", http.StatusInternalServerError)
				return
			}
			sent++
		}

		status := http.StatusOK
		message := "portal security notification cycle completed"
		if failed > 0 {
			status = http.StatusBadGateway
			message = "one or more portal security notifications will be retried"
		}
		writePortalNotificationWorkerResponse(
			w,
			status,
			materialized,
			sent,
			failed,
			message,
		)
	}
}

func writePortalNotificationWorkerResponse(
	w http.ResponseWriter,
	status int,
	materialized portal.OutboxMaterializationResult,
	sent int,
	failed int,
	message string,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"materialized": materialized.Created,
		"deferred":     materialized.Deferred,
		"selected":     materialized.Selected,
		"sent":         sent,
		"failed":       failed,
		"message":      message,
	})
}

func renderPortalSecurityNotification(
	notification portal.PendingNotification,
	baseURL string,
) (string, string, error) {
	clubName, ok := notification.Payload["club_name"].(string)
	if !ok || strings.TrimSpace(clubName) == "" {
		return "", "", fmt.Errorf("security notification has no club name")
	}
	clubName = strings.TrimSpace(clubName)
	parsedBaseURL, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsedBaseURL.Scheme != "https" || parsedBaseURL.Host == "" {
		return "", "", fmt.Errorf("security notification requires a configured HTTPS public base URL")
	}
	parsedBaseURL.Path = strings.TrimRight(parsedBaseURL.Path, "/") + "/portal/sessions"
	parsedBaseURL.RawPath = ""
	parsedBaseURL.RawQuery = ""
	parsedBaseURL.Fragment = ""
	securityURL := parsedBaseURL.String()

	switch notification.TemplateKey {
	case portal.NotificationTemplateAccountActivated:
		return "Your GMCL Club Operations Portal account is active", fmt.Sprintf(
			`Your named GMCL Club Operations Portal account has been activated for %s.

You can review and revoke active sessions here:
%s

If you did not complete this activation, contact the GMCL Club Liaison Officer immediately. Do not forward this email or share your account.

This is an account security notification. Email remains the official GMCL communication channel during the portal pilot.`,
			clubName,
			securityURL,
		), nil
	case portal.NotificationTemplateAccessRevoked:
		roleLabel := "club role"
		if rawRole, ok := notification.Payload["role"].(string); ok {
			if role, valid := portal.ParseRoleKey(rawRole); valid {
				roleLabel = humanPortalRole(role)
			}
		}
		return "Your GMCL Club Operations Portal access changed", fmt.Sprintf(
			`Your %s access for %s has been revoked.

Every active portal session using that appointment has been signed out. Other approved club appointments, if any, are unaffected.

If this change is unexpected, contact the GMCL Club Liaison Officer using the established official contact route. This email intentionally does not include an administrative reason or other private account details.

Email remains the official GMCL communication channel during the portal pilot.`,
			roleLabel,
			clubName,
		), nil
	default:
		return "", "", fmt.Errorf("unsupported portal security notification template")
	}
}
