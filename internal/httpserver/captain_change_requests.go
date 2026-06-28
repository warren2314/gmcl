package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const (
	captainChangePermanent = "permanent"
	captainChangeTemporary = "temporary"
)

type adminCaptainChangeRequestRow struct {
	ID                     int64
	TeamID                 int32
	CurrentCaptainID       int32
	ClubName               string
	TeamName               string
	CurrentCaptainName     string
	CurrentCaptainEmail    string
	RequestType            string
	NomineeName            string
	NomineeEmail           string
	NomineePhone           string
	EffectiveFrom          string
	EffectiveTo            string
	Reason                 string
	RequestedAt            time.Time
	NomineeActiveElsewhere bool
}

type captainChangeRequestForApproval struct {
	ID               int64
	TeamID           int32
	CurrentCaptainID int32
	RequestType      string
	NomineeName      string
	NomineeEmail     string
	NomineePhone     string
	EffectiveFrom    time.Time
	EffectiveTo      *time.Time
}

type captainChangeApprovalResult struct {
	RequestID        int64
	RequestType      string
	TeamID           int32
	OldCaptainID     int32
	NewCaptainID     int32
	RestoreCaptainID int32
	NomineeEmail     string
	RevokedTokens    int64
	DeletedDrafts    int64
	LinkSent         bool
	LinkEmail        string
	LinkWarning      string
}

func normaliseCaptainChangeRequestType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case captainChangePermanent:
		return captainChangePermanent
	case captainChangeTemporary:
		return captainChangeTemporary
	default:
		return ""
	}
}

func parseCaptainChangeDates(requestType, fromRaw, toRaw string) (time.Time, *time.Time, error) {
	effectiveFrom, err := time.Parse("2006-01-02", strings.TrimSpace(fromRaw))
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("effective-from date must use YYYY-MM-DD")
	}

	switch requestType {
	case captainChangePermanent:
		if strings.TrimSpace(toRaw) != "" {
			return time.Time{}, nil, fmt.Errorf("permanent changes must not have an end date")
		}
		return effectiveFrom, nil, nil
	case captainChangeTemporary:
		effectiveTo, err := time.Parse("2006-01-02", strings.TrimSpace(toRaw))
		if err != nil {
			return time.Time{}, nil, fmt.Errorf("temporary changes require an effective-to date using YYYY-MM-DD")
		}
		if effectiveTo.Before(effectiveFrom) {
			return time.Time{}, nil, fmt.Errorf("temporary effective-to date cannot be before effective-from date")
		}
		return effectiveFrom, &effectiveTo, nil
	default:
		return time.Time{}, nil, fmt.Errorf("request type must be permanent or temporary")
	}
}

func captainChangeActiveOnDate(effectiveFrom time.Time, effectiveTo *time.Time, day time.Time) bool {
	from := dateOnlyUTC(effectiveFrom)
	target := dateOnlyUTC(day)
	if target.Before(from) {
		return false
	}
	if effectiveTo == nil {
		return true
	}
	return !target.After(dateOnlyUTC(*effectiveTo))
}

func (s *Server) todayDate() time.Time {
	loc := s.LondonLoc
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Server) captainSessionStillActive(ctx context.Context, sess *captainSession) bool {
	if sess == nil || sess.CaptainID <= 0 || sess.TeamID <= 0 {
		return false
	}
	var ok bool
	_ = s.DB.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM captains c
			JOIN teams t ON t.id = c.team_id
			WHERE c.id = $1
			  AND c.team_id = $2
			  AND t.active = TRUE
			  AND c.active_from <= CURRENT_DATE
			  AND (c.active_to IS NULL OR c.active_to >= CURRENT_DATE)
		)
	`, sess.CaptainID, sess.TeamID).Scan(&ok)
	return ok
}

func (s *Server) handleCaptainChangeRequest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := getCaptainSessionFromRequest(r)
		if err != nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		if sess.SubmitterRole != "captain" {
			http.Error(w, "only the official captain can request captain changes", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
		defer cancel()
		if !s.captainSessionStillActive(ctx, sess) {
			http.Error(w, "captain session is no longer active", http.StatusForbidden)
			return
		}

		requestType := normaliseCaptainChangeRequestType(r.FormValue("request_type"))
		effectiveFrom, effectiveTo, err := parseCaptainChangeDates(requestType, r.FormValue("effective_from"), r.FormValue("effective_to"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		nomineeName := strings.TrimSpace(r.FormValue("nominee_name"))
		nomineeEmail := strings.ToLower(strings.TrimSpace(r.FormValue("nominee_email")))
		nomineePhone := strings.TrimSpace(r.FormValue("nominee_phone"))
		reason := strings.TrimSpace(r.FormValue("reason"))
		if nomineeName == "" || nomineeEmail == "" || !strings.Contains(nomineeEmail, "@") {
			http.Error(w, "nominee name and valid email are required", http.StatusBadRequest)
			return
		}

		var pendingExists bool
		if err := s.DB.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM captain_change_requests
				WHERE team_id = $1 AND status = 'pending'
			)
		`, sess.TeamID).Scan(&pendingExists); err != nil {
			http.Error(w, "could not check pending requests", http.StatusInternalServerError)
			return
		}
		if pendingExists {
			http.Error(w, "a captain change request is already pending for this team", http.StatusConflict)
			return
		}

		var requestID int64
		err = s.DB.QueryRow(ctx, `
			INSERT INTO captain_change_requests
			    (team_id, current_captain_id, request_type, nominee_full_name, nominee_email, nominee_phone,
			     effective_from, effective_to, reason)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, NULLIF($9, ''))
			RETURNING id
		`, sess.TeamID, sess.CaptainID, requestType, nomineeName, nomineeEmail, nomineePhone,
			effectiveFrom.Format("2006-01-02"), nullableDate(effectiveTo), reason).Scan(&requestID)
		if err != nil {
			http.Error(w, "could not create captain change request: "+err.Error(), http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "system", nil, "captain_change_requested", "captain_change_request", &requestID, map[string]any{
			"team_id":            sess.TeamID,
			"current_captain_id": sess.CaptainID,
			"request_type":       requestType,
			"nominee_email":      nomineeEmail,
			"effective_from":     effectiveFrom.Format("2006-01-02"),
			"effective_to":       nullableDateString(effectiveTo),
		})

		http.Redirect(w, r, "/captain/form?change_request=created", http.StatusSeeOther)
	}
}

func nullableDate(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format("2006-01-02")
}

func nullableDateString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func (s *Server) handleAdminCaptainChangeApprove() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		requestID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if requestID <= 0 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		adminID := s.resolveAdminID(r)
		result, err := s.approveCaptainChangeRequest(ctx, r, requestID, adminID)
		if err != nil {
			s.redirectTeamsCaptainsError(w, r, "Could not approve captain change: "+err.Error())
			return
		}

		message := "Captain change approved."
		if result.LinkSent {
			message += " Fresh link sent to " + result.LinkEmail + "."
		} else if result.LinkWarning != "" {
			message += " " + result.LinkWarning
		}
		s.redirectTeamsCaptainsSuccess(w, r, message)
	}
}

func (s *Server) loadPendingCaptainChangeRequests(ctx context.Context) ([]adminCaptainChangeRequestRow, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT ccr.id,
		       ccr.team_id,
		       ccr.current_captain_id,
		       cl.name,
		       t.name,
		       COALESCE(cur.full_name, ''),
		       COALESCE(cur.email, ''),
		       ccr.request_type,
		       ccr.nominee_full_name,
		       ccr.nominee_email,
		       COALESCE(ccr.nominee_phone, ''),
		       TO_CHAR(ccr.effective_from, 'YYYY-MM-DD'),
		       COALESCE(TO_CHAR(ccr.effective_to, 'YYYY-MM-DD'), ''),
		       COALESCE(ccr.reason, ''),
		       ccr.requested_at,
		       EXISTS (
		           SELECT 1
		           FROM captains other
		           WHERE other.team_id <> ccr.team_id
		             AND LOWER(other.email) = LOWER(ccr.nominee_email)
		             AND other.active_from <= CURRENT_DATE
		             AND (other.active_to IS NULL OR other.active_to >= CURRENT_DATE)
		       ) AS nominee_active_elsewhere
		FROM captain_change_requests ccr
		JOIN teams t ON t.id = ccr.team_id
		JOIN clubs cl ON cl.id = t.club_id
		LEFT JOIN captains cur ON cur.id = ccr.current_captain_id
		WHERE ccr.status = 'pending'
		ORDER BY ccr.requested_at ASC, ccr.id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []adminCaptainChangeRequestRow
	for rows.Next() {
		var row adminCaptainChangeRequestRow
		if err := rows.Scan(&row.ID, &row.TeamID, &row.CurrentCaptainID, &row.ClubName, &row.TeamName,
			&row.CurrentCaptainName, &row.CurrentCaptainEmail, &row.RequestType, &row.NomineeName,
			&row.NomineeEmail, &row.NomineePhone, &row.EffectiveFrom, &row.EffectiveTo, &row.Reason,
			&row.RequestedAt, &row.NomineeActiveElsewhere); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Server) handleAdminCaptainChangeReject() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		requestID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if requestID <= 0 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		note := strings.TrimSpace(r.FormValue("decision_note"))

		ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
		defer cancel()
		adminID := s.resolveAdminID(r)
		var adminIDArg any
		if adminID != nil {
			adminIDArg = *adminID
		}
		tag, err := s.DB.Exec(ctx, `
			UPDATE captain_change_requests
			SET status = 'rejected',
			    decided_at = now(),
			    decided_by_admin_id = $2,
			    decision_note = NULLIF($3, '')
			WHERE id = $1 AND status = 'pending'
		`, requestID, adminIDArg, note)
		if err != nil {
			s.redirectTeamsCaptainsError(w, r, "Could not reject captain change: "+err.Error())
			return
		}
		if tag.RowsAffected() == 0 {
			s.redirectTeamsCaptainsError(w, r, "Captain change request is not pending.")
			return
		}
		s.audit(ctx, r, "admin", adminID, "captain_change_rejected", "captain_change_request", &requestID, map[string]any{
			"decision_note": note,
		})
		s.redirectTeamsCaptainsSuccess(w, r, "Captain change request rejected.")
	}
}

func (s *Server) approveCaptainChangeRequest(ctx context.Context, r *http.Request, requestID int64, adminID *int32) (captainChangeApprovalResult, error) {
	var result captainChangeApprovalResult
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)

	req, err := loadCaptainChangeRequestForApproval(ctx, tx, requestID)
	if err != nil {
		return result, err
	}
	result.RequestID = req.ID
	result.RequestType = req.RequestType
	result.TeamID = req.TeamID
	result.OldCaptainID = req.CurrentCaptainID
	result.NomineeEmail = req.NomineeEmail

	// Lock all captain rows for the team before changing any date windows.
	rows, err := tx.Query(ctx, `SELECT id FROM captains WHERE team_id = $1 FOR UPDATE`, req.TeamID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()

	var displacedName, displacedEmail, displacedPhone string
	var displacedEmailOverride sql.NullString
	var displacedEmailOverrideUntil sql.NullTime
	if err := tx.QueryRow(ctx, `
		SELECT full_name, email, COALESCE(phone, ''), email_override, email_override_until
		FROM captains
		WHERE id = $1 AND team_id = $2
	`, req.CurrentCaptainID, req.TeamID).Scan(&displacedName, &displacedEmail, &displacedPhone, &displacedEmailOverride, &displacedEmailOverrideUntil); err != nil {
		return result, fmt.Errorf("could not load displaced captain: %w", err)
	}

	closeDate := req.EffectiveFrom.AddDate(0, 0, -1)
	closeTag, err := tx.Exec(ctx, `
		UPDATE captains
		SET active_to = $2
		WHERE team_id = $1
		  AND active_from <= $3
		  AND (active_to IS NULL OR active_to >= $3)
	`, req.TeamID, closeDate.Format("2006-01-02"), req.EffectiveFrom.Format("2006-01-02"))
	if err != nil {
		return result, fmt.Errorf("could not close current captain window: %w", err)
	}
	if closeTag.RowsAffected() == 0 {
		return result, fmt.Errorf("no active captain window overlaps the requested effective date")
	}

	var activeTo any
	if req.RequestType == captainChangeTemporary && req.EffectiveTo != nil {
		activeTo = req.EffectiveTo.Format("2006-01-02")
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO captains (team_id, full_name, email, phone, active_from, active_to)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6)
		RETURNING id
	`, req.TeamID, req.NomineeName, req.NomineeEmail, req.NomineePhone,
		req.EffectiveFrom.Format("2006-01-02"), activeTo).Scan(&result.NewCaptainID); err != nil {
		return result, fmt.Errorf("could not create approved captain: %w", err)
	}

	if req.RequestType == captainChangeTemporary && req.EffectiveTo != nil {
		restoreFrom := req.EffectiveTo.AddDate(0, 0, 1)
		var emailOverride any
		if displacedEmailOverride.Valid {
			emailOverride = displacedEmailOverride.String
		}
		var emailOverrideUntil any
		if displacedEmailOverrideUntil.Valid {
			emailOverrideUntil = displacedEmailOverrideUntil.Time.Format("2006-01-02")
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO captains
			    (team_id, full_name, email, phone, active_from, active_to, email_override, email_override_until)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULL, $6, $7)
			RETURNING id
		`, req.TeamID, displacedName, displacedEmail, displacedPhone, restoreFrom.Format("2006-01-02"),
			emailOverride, emailOverrideUntil).Scan(&result.RestoreCaptainID); err != nil {
			return result, fmt.Errorf("could not create captain restore row: %w", err)
		}
	}

	var adminIDArg any
	if adminID != nil {
		adminIDArg = *adminID
	}
	if _, err := tx.Exec(ctx, `
		UPDATE captain_change_requests
		SET status = 'approved',
		    decided_at = now(),
		    decided_by_admin_id = $2,
		    approved_captain_id = $3
		WHERE id = $1 AND status = 'pending'
	`, req.ID, adminIDArg, result.NewCaptainID); err != nil {
		return result, fmt.Errorf("could not mark request approved: %w", err)
	}

	activeToday := captainChangeActiveOnDate(req.EffectiveFrom, req.EffectiveTo, s.todayDate())
	if activeToday {
		tag, err := tx.Exec(ctx, `
			UPDATE magic_link_tokens
			SET used_at = now()
			WHERE captain_id = $1
			  AND used_at IS NULL
			  AND expires_at > now()
		`, req.CurrentCaptainID)
		if err != nil {
			return result, fmt.Errorf("could not revoke old captain links: %w", err)
		}
		result.RevokedTokens = tag.RowsAffected()

		draftTag, err := tx.Exec(ctx, `
			DELETE FROM drafts
			WHERE team_id = $1
			  AND week_id IN (
			      SELECT id FROM weeks WHERE CURRENT_DATE BETWEEN start_date AND end_date
			  )
		`, req.TeamID)
		if err != nil {
			return result, fmt.Errorf("could not delete current draft: %w", err)
		}
		result.DeletedDrafts = draftTag.RowsAffected()
	}

	if err := tx.Commit(ctx); err != nil {
		return result, err
	}

	s.audit(ctx, r, "admin", adminID, "captain_change_approved", "captain_change_request", &req.ID, map[string]any{
		"team_id":            req.TeamID,
		"old_captain_id":     req.CurrentCaptainID,
		"new_captain_id":     result.NewCaptainID,
		"restore_captain_id": result.RestoreCaptainID,
		"request_type":       req.RequestType,
		"effective_from":     req.EffectiveFrom.Format("2006-01-02"),
		"effective_to":       nullableDateString(req.EffectiveTo),
		"revoked_tokens":     result.RevokedTokens,
		"deleted_drafts":     result.DeletedDrafts,
	})
	s.audit(ctx, r, "admin", adminID, "captain_deactivated_for_change", "captain", func() *int64 {
		v := int64(req.CurrentCaptainID)
		return &v
	}(), map[string]any{"captain_change_request_id": req.ID, "active_to": closeDate.Format("2006-01-02")})
	s.audit(ctx, r, "admin", adminID, "captain_created_from_change", "captain", func() *int64 {
		v := int64(result.NewCaptainID)
		return &v
	}(), map[string]any{"captain_change_request_id": req.ID, "request_type": req.RequestType})

	if !activeToday {
		result.LinkWarning = "The approved captain is not active yet, so no access link was sent."
		return result, nil
	}
	linkEmail, err := s.sendFreshCaptainAccessLink(ctx, r, result.NewCaptainID)
	if err != nil {
		result.LinkWarning = "Captain was changed, but the fresh access link could not be sent: " + err.Error()
		return result, nil
	}
	result.LinkSent = true
	result.LinkEmail = linkEmail
	s.audit(ctx, r, "admin", adminID, "captain_change_access_link_sent", "captain", func() *int64 {
		v := int64(result.NewCaptainID)
		return &v
	}(), map[string]any{"captain_change_request_id": req.ID, "email": linkEmail})
	return result, nil
}

func loadCaptainChangeRequestForApproval(ctx context.Context, tx pgx.Tx, requestID int64) (captainChangeRequestForApproval, error) {
	var req captainChangeRequestForApproval
	var effectiveTo sql.NullTime
	err := tx.QueryRow(ctx, `
		SELECT id, team_id, current_captain_id, request_type,
		       nominee_full_name, nominee_email, COALESCE(nominee_phone, ''),
		       effective_from, effective_to
		FROM captain_change_requests
		WHERE id = $1 AND status = 'pending'
		FOR UPDATE
	`, requestID).Scan(&req.ID, &req.TeamID, &req.CurrentCaptainID, &req.RequestType,
		&req.NomineeName, &req.NomineeEmail, &req.NomineePhone, &req.EffectiveFrom, &effectiveTo)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return req, fmt.Errorf("captain change request is not pending")
		}
		return req, err
	}
	if effectiveTo.Valid {
		req.EffectiveTo = &effectiveTo.Time
	}
	return req, nil
}
