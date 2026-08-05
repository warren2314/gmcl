package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sanctiondomain "cricket-ground-feedback/internal/sanctions"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func parseApprovedIneligibleReopenForm(caseIDRaw, reasonRaw string) (int64, string, error) {
	caseID, err := strconv.ParseInt(strings.TrimSpace(caseIDRaw), 10, 64)
	reason := strings.TrimSpace(reasonRaw)
	if err != nil || caseID < 1 {
		return 0, "", errors.New("a valid case is required")
	}
	if reason == "" || len(reason) > 4000 {
		return 0, "", errors.New("an audit reason of at most 4000 characters is required")
	}
	return caseID, reason, nil
}

func adminIneligibleReopenFormHTML(caseID int64, csrf string) string {
	return fmt.Sprintf(`<form method="POST" action="/admin/cases/%d/reopen-source-change" class="card mb-3 border-warning"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">New source revision after approval</div><div class="card-body"><p class="mb-2">The linked intake changed after this outcome was approved. Reopening is allowed only before publication or delivery. It preserves the approved record, appends compensating card-ledger entries, cancels untouched follow-up work, and revokes unsent outcome items.</p><p class="small text-muted">After reopening, review and merge the latest intake revision, then create a fresh proposal for approval by a different authorised administrator.</p><label class="form-label">Reason for reopening</label><textarea class="form-control" name="reason" rows="3" maxlength="4000" required></textarea></div><div class="card-footer"><button class="btn btn-warning">Reopen for source review</button></div></form>`, caseID, escapeHTML(csrf))
}

func (s *Server) writeAdminIneligibleReopenControl(w http.ResponseWriter, r *http.Request, caseID int64, sourceType, status, csrf string) {
	if sourceType != "ineligible_player" || status != "approved" {
		return
	}
	actor := adminActor(r)
	if actor.ID == nil || (!s.adminHasPermission(r.Context(), *actor.ID, "sanctions_investigate") && !s.adminHasPermission(r.Context(), *actor.ID, "sanctions_approve")) {
		return
	}
	ready, err := sanctiondomain.NewService(s.DB).ApprovedIneligibleCaseNeedsReopen(r.Context(), caseID)
	if err != nil || !ready {
		return
	}
	fmt.Fprint(w, adminIneligibleReopenFormHTML(caseID, csrf))
}

func (s *Server) handleAdminCaseReopenSourceChange() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid reopen request", http.StatusBadRequest)
			return
		}
		caseID, reason, err := parseApprovedIneligibleReopenForm(chi.URLParam(r, "id"), r.FormValue("reason"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		err = sanctiondomain.NewService(s.DB).ReopenApprovedIneligibleCase(r.Context(), caseID, actor, reason)
		if err != nil {
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				http.NotFound(w, r)
			case errors.Is(err, sanctiondomain.ErrIneligibleReopenNotAllowed):
				http.Error(w, "only an approved, unpublished ineligible-player case can be reopened", http.StatusConflict)
			case errors.Is(err, sanctiondomain.ErrIneligibleReopenNoSourceChange):
				http.Error(w, "no newer linked intake revision requires review", http.StatusConflict)
			case errors.Is(err, sanctiondomain.ErrIneligibleReopenAlreadySent):
				http.Error(w, "the outcome or compatibility notice may already have been sent; reopening is blocked", http.StatusConflict)
			case errors.Is(err, sanctiondomain.ErrIneligibleReopenActionStarted):
				http.Error(w, "approved follow-up work has already started; reopening is blocked", http.StatusConflict)
			case errors.Is(err, sanctiondomain.ErrIneligibleReopenOutboxBusy):
				http.Error(w, "outcome delivery is currently being processed; retry after the outbox run finishes", http.StatusConflict)
			default:
				http.Error(w, "case could not be reopened", http.StatusInternalServerError)
			}
			return
		}
		message := "Approved outcome reopened. Review and merge the latest intake revision before preparing a fresh independently approved decision."
		query := url.Values{"success": []string{message}}
		target := fmt.Sprintf("/admin/cases/%d?%s", caseID, query.Encode())
		if s.adminHasPermission(r.Context(), *actor.ID, "sanctions_triage") {
			query.Set("state", "exception")
			target = "/admin/ineligible?" + query.Encode()
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}
