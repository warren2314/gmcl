package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ineligibledomain "cricket-ground-feedback/internal/ineligible"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const maxIneligibleBackfillUpload = 16 << 20

type ineligibleBackfillRunView struct {
	ID                    int64
	Filename              string
	StorageKey            string
	SHA256                string
	RowsTotal             int
	MatchedExact          int
	MatchedNormalized     int
	Unmatched             int
	Ambiguous             int
	Invalid               int
	WithManualHistory     int
	RequiringEffectReview int
	CreatedAt             time.Time
	SignatoryName         string
	SignedOffAt           *time.Time
}

type ineligibleBackfillRowView struct {
	ID                   int64
	SourceRowNumber      int
	SubmittedAt          *time.Time
	FixtureDate          *time.Time
	Player               string
	OffendingClub        string
	Team                 string
	MatchStatus          string
	MatchedIntakeID      *int64
	CandidateIntakeIDs   []int64
	Exception            string
	TrackerStateHint     string
	PointsText           string
	CardsText            string
	RequiresEffectReview bool
	ManualHistory        map[string]string
	ReviewID             *int64
	ReviewDisposition    string
	ReviewedIntakeID     *int64
	ReviewedCaseState    string
	EffectsReviewStatus  string
	EffectInterpretation string
	ReviewReason         string
	ReviewerName         string
	ReviewedAt           *time.Time
}

func (s *Server) handleAdminIneligibleBackfills() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		showVerified := r.URL.Query().Get("view") == "verified"
		rows, err := s.DB.Query(ctx, `
			SELECT r.id,r.source_filename,r.storage_key,r.source_sha256,r.rows_total,
			       r.rows_matched_exact,r.rows_matched_normalized,r.rows_unmatched,
			       r.rows_ambiguous,r.rows_invalid,r.rows_with_manual_history,
			       r.rows_requiring_effect_review,r.created_at,
			       COALESCE(signoff.signatory_name,''),signoff.created_at
			FROM sanction_ineligible_backfill_runs r
			LEFT JOIN LATERAL (
				SELECT signatory_name,created_at
				FROM sanction_ineligible_backfill_signoffs s
				WHERE s.run_id=r.id ORDER BY s.id DESC LIMIT 1
			) signoff ON TRUE
			WHERE (($1 AND signoff.created_at IS NOT NULL) OR (NOT $1 AND signoff.created_at IS NULL))
			ORDER BY r.id DESC LIMIT 100
		`, showVerified)
		if err != nil {
			http.Error(w, "tracker reconciliation is unavailable", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		runs := make([]ineligibleBackfillRunView, 0, 8)
		for rows.Next() {
			var run ineligibleBackfillRunView
			if err = rows.Scan(&run.ID, &run.Filename, &run.StorageKey, &run.SHA256, &run.RowsTotal,
				&run.MatchedExact, &run.MatchedNormalized, &run.Unmatched, &run.Ambiguous,
				&run.Invalid, &run.WithManualHistory, &run.RequiringEffectReview,
				&run.CreatedAt, &run.SignatoryName, &run.SignedOffAt); err != nil {
				http.Error(w, "could not read tracker reconciliation", http.StatusInternalServerError)
				return
			}
			runs = append(runs, run)
		}
		if err = rows.Err(); err != nil {
			http.Error(w, "could not read tracker reconciliation", http.StatusInternalServerError)
			return
		}

		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Ineligible-player tracker backfill")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 py-4"><div class="d-flex flex-column flex-md-row justify-content-between gap-2 mb-3"><div><h1 class="h2 mb-1">Excel import</h1><p class="text-muted mb-0">Upload the historical tracker, check each match, and confirm how its information should be recorded.</p></div><a class="btn btn-outline-secondary align-self-md-start" href="/admin/ineligible">Back to work queue</a></div>`)
		writeIneligibleBackfillFlash(w, r)
		pendingClass, verifiedClass := "btn-outline-primary", "btn-outline-primary"
		if showVerified {
			verifiedClass = "btn-primary"
		} else {
			pendingClass = "btn-primary"
		}
		fmt.Fprintf(w, `<nav class="btn-group mb-4" aria-label="Choose imports"><a class="btn %s" href="/admin/ineligible/backfill">Needs checking</a><a class="btn %s" href="/admin/ineligible/backfill?view=verified">Verified history</a></nav>`, pendingClass, verifiedClass)
		fmt.Fprintf(w, `<section class="alert alert-warning"><strong>Safe historical import.</strong> Uploading and checking the workbook cannot email anyone, change a live case, or add points. Changes are applied only after every row has been checked and the import is signed off.</section><section class="card shadow-sm mb-4"><div class="card-header fw-semibold">Upload a tracker workbook</div><form method="POST" action="/admin/ineligible/backfill" enctype="multipart/form-data"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3 align-items-end"><div class="col-lg-8"><label class="form-label">Tracker (.xlsx, max 16 MB)</label><input class="form-control" type="file" name="tracker" accept=".xlsx,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" required><div class="form-text">Use the <em>Form responses 1</em> sheet with columns A to Z. GMCL saves a technical file fingerprint so the original upload can always be identified.</div></div><div class="col-lg-4"><button class="btn btn-primary w-100">Upload and check</button></div></div></form></section>`, escapeHTML(csrf))
		fmt.Fprint(w, `<section class="card shadow-sm"><div class="card-header fw-semibold">Import checks</div><div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0"><thead><tr><th>Run</th><th>Source</th><th>Rows</th><th>Matches</th><th>Exceptions</th><th>Manual history</th><th>Verification</th></tr></thead><tbody>`)
		for _, run := range runs {
			signoff := `<span class="badge text-bg-warning">Not signed off</span>`
			if run.SignedOffAt != nil {
				signoff = fmt.Sprintf(`<span class="badge text-bg-success">Signed off</span><div class="small mt-1">%s<br>%s</div>`, escapeHTML(run.SignatoryName), escapeHTML(run.SignedOffAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")))
			}
			fmt.Fprintf(w, `<tr><td data-label="Run"><a href="/admin/ineligible/backfill/%d"><strong>#%d</strong></a><div class="small text-muted">%s</div></td><td data-label="Source">%s<div class="small"><code>%s</code></div></td><td data-label="Rows">%d</td><td data-label="Matches">%d exact<br><span class="small text-muted">%d normalised</span></td><td data-label="Exceptions">%d unmatched, %d ambiguous, %d invalid</td><td data-label="Manual history">%d rows<div class="small text-danger">%d effect reviews</div></td><td data-label="Sign-off">%s</td></tr>`,
				run.ID, run.ID, escapeHTML(run.CreatedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")),
				escapeHTML(run.Filename), escapeHTML(shortHash(run.SHA256)), run.RowsTotal,
				run.MatchedExact, run.MatchedNormalized, run.Unmatched, run.Ambiguous, run.Invalid,
				run.WithManualHistory, run.RequiringEffectReview, signoff)
		}
		if len(runs) == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-center text-muted py-5">Nothing to show in this view.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></section></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminIneligibleBackfillUpload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxIneligibleBackfillUpload+(1<<20))
		if err := r.ParseMultipartForm(maxIneligibleBackfillUpload); err != nil {
			redirectIneligibleBackfill(w, r, "/admin/ineligible/backfill", "error", "The tracker upload is invalid or exceeds 16 MB.")
			return
		}
		file, header, err := r.FormFile("tracker")
		if err != nil {
			redirectIneligibleBackfill(w, r, "/admin/ineligible/backfill", "error", "Choose a tracker workbook to upload.")
			return
		}
		defer file.Close()
		if !strings.EqualFold(filepath.Ext(header.Filename), ".xlsx") {
			redirectIneligibleBackfill(w, r, "/admin/ineligible/backfill", "error", "The tracker must be an .xlsx workbook.")
			return
		}
		data, err := io.ReadAll(io.LimitReader(file, maxIneligibleBackfillUpload+1))
		if err != nil || len(data) == 0 || len(data) > maxIneligibleBackfillUpload {
			redirectIneligibleBackfill(w, r, "/admin/ineligible/backfill", "error", "The tracker could not be read or exceeds 16 MB.")
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		adminID := int64(*actor.ID)
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		summary, err := ineligibledomain.StageTrackerWorkbook(ctx, s.DB, header.Filename, data, &adminID, s.LondonLoc)
		if err != nil {
			redirectIneligibleBackfill(w, r, "/admin/ineligible/backfill", "error", "Tracker rejected: "+truncateBackfillMessage(err.Error(), 500))
			return
		}
		message := fmt.Sprintf("Run %d staged: %d rows; %d exact, %d normalised, %d unmatched, %d ambiguous and %d invalid.", summary.RunID, summary.RowsTotal, summary.MatchedExact, summary.MatchedNormalized, summary.Unmatched, summary.Ambiguous, summary.Invalid)
		redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", summary.RunID), "success", message)
	}
}

func (s *Server) handleAdminIneligibleBackfillRun() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, ok := positiveBackfillParam(r, "runID")
		if !ok {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		run, err := s.loadIneligibleBackfillRun(ctx, runID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		rows, err := s.loadIneligibleBackfillRows(ctx, runID)
		if err != nil {
			http.Error(w, "could not load tracker rows", http.StatusInternalServerError)
			return
		}
		showVerified := r.URL.Query().Get("view") == "verified"
		displayRows := make([]ineligibleBackfillRowView, 0, len(rows))
		pendingCount, verifiedCount := 0, 0
		for _, row := range rows {
			if row.ReviewID == nil {
				pendingCount++
				if !showVerified {
					displayRows = append(displayRows, row)
				}
			} else {
				verifiedCount++
				if showVerified {
					displayRows = append(displayRows, row)
				}
			}
		}
		readiness, _ := s.loadIneligibleBackfillReadiness(ctx, runID)
		applyPreview, applyPreviewErr := ineligibledomain.LoadTrackerBackfillApplyPreview(ctx, s.DB, runID)
		csrf := middleware.CSRFToken(r)
		actor := adminActor(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, fmt.Sprintf("Tracker reconciliation %d", runID))
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container-fluid px-3 px-lg-4 py-4"><div class="d-flex flex-column flex-md-row justify-content-between gap-2 mb-3"><div><h1 class="h2 mb-1">Import check #%d</h1><p class="text-muted mb-0">%s · file fingerprint <code>%s</code></p></div><div class="d-flex gap-2 align-self-md-start"><a class="btn btn-outline-secondary" href="/admin/ineligible/backfill">All imports</a><a class="btn btn-outline-primary" href="/admin/ineligible/backfill/%d/source">Download original</a></div></div>`, run.ID, escapeHTML(run.Filename), escapeHTML(shortHash(run.SHA256)), run.ID)
		writeIneligibleBackfillFlash(w, r)
		fmt.Fprintf(w, `<div class="row row-cols-2 row-cols-md-4 row-cols-xl-7 g-3 mb-4">`)
		for _, card := range []struct {
			label string
			value int
		}{
			{"Rows", run.RowsTotal}, {"Exact", run.MatchedExact}, {"Normalised", run.MatchedNormalized},
			{"Unmatched", run.Unmatched}, {"Ambiguous", run.Ambiguous}, {"Invalid", run.Invalid},
			{"Effects to review", run.RequiringEffectReview},
		} {
			fmt.Fprintf(w, `<div class="col"><div class="card h-100"><div class="card-body py-3"><div class="h3 mb-0">%d</div><div class="small text-muted">%s</div></div></div></div>`, card.value, escapeHTML(card.label))
		}
		fmt.Fprint(w, `</div><div class="alert alert-info"><strong>How this works:</strong> check the suggested match and choose how the tracker row should be treated. Confirming a row records your check; it does not email anyone, change a live case, or add points.</div>`)
		pendingClass, verifiedClass := "btn-outline-primary", "btn-outline-primary"
		if showVerified {
			verifiedClass = "btn-primary"
		} else {
			pendingClass = "btn-primary"
		}
		fmt.Fprintf(w, `<nav class="btn-group mb-3" aria-label="Choose import rows"><a class="btn %s" href="/admin/ineligible/backfill/%d">Needs checking <span class="badge text-bg-light ms-1">%d</span></a><a class="btn %s" href="/admin/ineligible/backfill/%d?view=verified">Verified history <span class="badge text-bg-light ms-1">%d</span></a></nav>`, pendingClass, runID, pendingCount, verifiedClass, runID, verifiedCount)
		fmt.Fprint(w, `<section class="d-grid gap-3">`)
		for _, row := range displayRows {
			writeIneligibleBackfillRow(w, csrf, actor.Label, runID, row, s.LondonLoc)
		}
		if len(displayRows) == 0 {
			fmt.Fprint(w, `<div class="card card-body text-center text-muted py-5">Nothing to show in this view.</div>`)
		}
		fmt.Fprint(w, `</section>`)
		if readiness.RowsTotal > 0 {
			alert := "alert-warning"
			message := fmt.Sprintf("%d/%d rows reviewed; %d state reviews and %d effect interpretations remain.", readiness.RowsReviewed, readiness.RowsTotal, readiness.RowsNeedingStateReview, readiness.RowsNeedingEffectReview)
			if readiness.Validate() == nil {
				alert = "alert-success"
				message = "Every staged row has an explicit state and effect interpretation. This run is ready for named sign-off."
			}
			fmt.Fprintf(w, `<section class="card shadow-sm mt-4"><div class="card-header fw-semibold">Final verification</div><div class="card-body"><div class="alert %s">%s</div><form method="POST" action="/admin/ineligible/backfill/%d/signoff"><input type="hidden" name="csrf_token" value="%s"><div class="row g-3"><div class="col-md-4"><label class="form-label">Your name</label><input class="form-control" name="signatory_name" value="%s" maxlength="200" required></div><div class="col-md-8"><label class="form-label">Confirmation</label><textarea class="form-control" name="statement" rows="2" maxlength="2000" required>I confirm that the tracker rows, suggested matches, open or closed states, and any points or cards notes have been checked. This check has not sent an email or changed points.</textarea></div><div class="col-12"><label class="form-check"><input class="form-check-input" type="checkbox" name="confirm" value="yes" required> <span class="form-check-label">Save my name and confirmation in the audit history. It cannot be edited later.</span></label></div></div><button class="btn btn-success mt-3"%s>Verify import</button></form></div></section>`, alert, escapeHTML(message), runID, escapeHTML(csrf), escapeHTML(actor.Label), backfillDisabledIf(readiness.Validate() != nil))
		}
		writeIneligibleBackfillApplication(w, csrf, actor.Label, applyPreview, applyPreviewErr, s.LondonLoc)
		fmt.Fprint(w, `</main>`)
		pageFooter(w)
	}
}

func (s *Server) loadIneligibleBackfillRun(ctx context.Context, runID int64) (ineligibleBackfillRunView, error) {
	var run ineligibleBackfillRunView
	err := s.DB.QueryRow(ctx, `
		SELECT id,source_filename,storage_key,source_sha256,rows_total,
		       rows_matched_exact,rows_matched_normalized,rows_unmatched,rows_ambiguous,
		       rows_invalid,rows_with_manual_history,rows_requiring_effect_review,created_at
		FROM sanction_ineligible_backfill_runs WHERE id=$1
	`, runID).Scan(&run.ID, &run.Filename, &run.StorageKey, &run.SHA256, &run.RowsTotal,
		&run.MatchedExact, &run.MatchedNormalized, &run.Unmatched, &run.Ambiguous,
		&run.Invalid, &run.WithManualHistory, &run.RequiringEffectReview, &run.CreatedAt)
	return run, err
}

func (s *Server) loadIneligibleBackfillRows(ctx context.Context, runID int64) ([]ineligibleBackfillRowView, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT br.id,br.source_row_number,br.submitted_at,br.fixture_date,
		       COALESCE(br.player_text,''),COALESCE(br.offending_club_text,''),COALESCE(br.team_text,''),
		       br.match_status,br.matched_intake_id,br.candidate_intake_ids::text,
		       COALESCE(br.exception_message,''),br.tracker_state_hint,
		       COALESCE(br.points_text,''),COALESCE(br.cards_text,''),br.requires_effect_review,
		       br.manual_history::text,review.id,COALESCE(review.disposition,''),review.reviewed_intake_id,
		       COALESCE(review.reviewed_case_state,''),COALESCE(review.effects_review_status,''),
		       COALESCE(review.effect_interpretation,''),COALESCE(review.review_reason,''),
		       COALESCE(review.reviewed_by_name,''),review.created_at
		FROM sanction_ineligible_backfill_rows br
		LEFT JOIN LATERAL (
			SELECT id,disposition,reviewed_intake_id,reviewed_case_state,effects_review_status,
			       effect_interpretation,review_reason,reviewed_by_name,created_at
			FROM sanction_ineligible_backfill_reviews rv
			WHERE rv.backfill_row_id=br.id ORDER BY rv.id DESC LIMIT 1
		) review ON TRUE
		WHERE br.run_id=$1 ORDER BY br.source_row_number
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ineligibleBackfillRowView, 0, 64)
	for rows.Next() {
		var row ineligibleBackfillRowView
		var candidatesJSON, manualJSON []byte
		if err = rows.Scan(&row.ID, &row.SourceRowNumber, &row.SubmittedAt, &row.FixtureDate,
			&row.Player, &row.OffendingClub, &row.Team, &row.MatchStatus, &row.MatchedIntakeID,
			&candidatesJSON, &row.Exception, &row.TrackerStateHint, &row.PointsText, &row.CardsText,
			&row.RequiresEffectReview, &manualJSON, &row.ReviewID, &row.ReviewDisposition,
			&row.ReviewedIntakeID, &row.ReviewedCaseState, &row.EffectsReviewStatus,
			&row.EffectInterpretation, &row.ReviewReason, &row.ReviewerName, &row.ReviewedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(candidatesJSON, &row.CandidateIntakeIDs)
		_ = json.Unmarshal(manualJSON, &row.ManualHistory)
		result = append(result, row)
	}
	return result, rows.Err()
}

func writeIneligibleBackfillRow(w io.Writer, csrf, defaultReviewer string, runID int64, row ineligibleBackfillRowView, loc *time.Location) {
	statusClass := map[string]string{"matched_exact": "text-bg-success", "matched_normalized": "text-bg-info", "unmatched": "text-bg-warning", "ambiguous": "text-bg-danger", "invalid": "text-bg-danger"}[row.MatchStatus]
	if statusClass == "" {
		statusClass = "text-bg-secondary"
	}
	when := "-"
	if row.SubmittedAt != nil {
		when = row.SubmittedAt.In(loc).Format("02 Jan 2006 15:04:05")
	}
	fixture := "-"
	if row.FixtureDate != nil {
		fixture = row.FixtureDate.Format("02 Jan 2006")
	}
	matched := int64(0)
	if row.ReviewedIntakeID != nil {
		matched = *row.ReviewedIntakeID
	} else if row.ReviewID == nil && row.MatchedIntakeID != nil {
		matched = *row.MatchedIntakeID
	}
	disposition := row.ReviewDisposition
	if disposition == "" {
		if row.MatchedIntakeID != nil {
			disposition = "accept_match"
		} else {
			disposition = "leave_unmatched"
		}
	}
	caseState := row.ReviewedCaseState
	if caseState == "" {
		caseState = row.TrackerStateHint
		if caseState == "unknown" {
			caseState = "needs_interpretation"
		}
	}
	effectStatus := row.EffectsReviewStatus
	if effectStatus == "" {
		if row.RequiresEffectReview {
			effectStatus = "pending_manual_interpretation"
		} else {
			effectStatus = "not_applicable"
		}
	}
	reviewer := row.ReviewerName
	if reviewer == "" {
		reviewer = defaultReviewer
	}
	candidates := make([]string, 0, len(row.CandidateIntakeIDs))
	for _, id := range row.CandidateIntakeIDs {
		candidates = append(candidates, strconv.FormatInt(id, 10))
	}
	reviewBadge := `<span class="badge text-bg-warning">Review required</span>`
	if row.ReviewID != nil {
		reviewBadge = `<span class="badge text-bg-primary">Reviewed</span>`
	}
	fmt.Fprintf(w, `<article class="card shadow-sm"><div class="card-header d-flex flex-column flex-lg-row justify-content-between gap-2"><div><strong>Source row %d · %s</strong><div class="small text-muted">%s · fixture %s</div></div><div><span class="badge %s">%s</span> %s</div></div><div class="card-body"><div class="row g-3"><div class="col-md-4"><div class="small text-muted">Player</div><strong>%s</strong></div><div class="col-md-4"><div class="small text-muted">Offending club</div><strong>%s</strong></div><div class="col-md-4"><div class="small text-muted">Team</div><strong>%s</strong></div></div>`, row.SourceRowNumber, escapeHTML(row.Player), escapeHTML(when), escapeHTML(fixture), statusClass, escapeHTML(strings.ReplaceAll(row.MatchStatus, "_", " ")), reviewBadge, escapeHTML(row.Player), escapeHTML(row.OffendingClub), escapeHTML(row.Team))
	if row.Exception != "" {
		fmt.Fprintf(w, `<div class="alert alert-warning py-2 mt-3 mb-0">%s`, escapeHTML(row.Exception))
		if len(candidates) > 0 {
			fmt.Fprintf(w, `<div class="small mt-1">Nearby candidate intake IDs: %s</div>`, escapeHTML(strings.Join(candidates, ", ")))
		}
		fmt.Fprint(w, `</div>`)
	}
	writeIneligibleManualHistory(w, row)
	fmt.Fprintf(w, `<hr><form method="POST" action="/admin/ineligible/backfill/%d/rows/%d/review"><input type="hidden" name="csrf_token" value="%s"><div class="row g-3"><div class="col-lg-3"><label class="form-label">Reconciliation</label><select class="form-select" name="disposition">`, runID, row.ID, escapeHTML(csrf))
	writeBackfillOption(w, "accept_match", "Accept intake match", disposition)
	writeBackfillOption(w, "leave_unmatched", "Leave unmatched for triage", disposition)
	writeBackfillOption(w, "exclude_tracker_row", "Exclude tracker row", disposition)
	fmt.Fprintf(w, `</select></div><div class="col-lg-3"><label class="form-label">Google intake ID</label><input class="form-control" type="number" min="1" name="intake_id" value="%s"><div class="form-text">Required only for accepted matches.</div></div><div class="col-lg-3"><label class="form-label">Historical case state</label><select class="form-select" name="case_state">`, valueIfPositive(matched))
	writeBackfillOption(w, "open", "Open", caseState)
	writeBackfillOption(w, "closed", "Closed", caseState)
	writeBackfillOption(w, "needs_interpretation", "Needs interpretation", caseState)
	fmt.Fprint(w, `</select></div><div class="col-lg-3"><label class="form-label">Points/cards review</label><select class="form-select" name="effects_status">`)
	if row.RequiresEffectReview {
		writeBackfillOption(w, "pending_manual_interpretation", "Pending manual interpretation", effectStatus)
		writeBackfillOption(w, "manually_interpreted", "Manually interpreted", effectStatus)
		writeBackfillOption(w, "confirmed_no_effect", "Confirmed no ledger effect", effectStatus)
	} else {
		writeBackfillOption(w, "not_applicable", "Not applicable", effectStatus)
	}
	fmt.Fprintf(w, `</select></div><div class="col-md-6"><label class="form-label">What do the points or cards notes mean?</label><textarea class="form-control" name="effect_interpretation" rows="3" maxlength="5000" placeholder="Record your reading. This does not add points or cards.">%s</textarea></div><div class="col-md-6"><label class="form-label">Why did you choose this?</label><textarea class="form-control" name="review_reason" rows="3" maxlength="5000" required>%s</textarea></div><div class="col-md-6"><label class="form-label">Checked by</label><input class="form-control" name="reviewer_name" value="%s" maxlength="200" required></div><div class="col-md-6 d-flex align-items-end"><button class="btn btn-primary w-100">Verify row</button></div></div></form></div></article>`, escapeHTML(row.EffectInterpretation), escapeHTML(row.ReviewReason), escapeHTML(reviewer))
}

func writeIneligibleManualHistory(w io.Writer, row ineligibleBackfillRowView) {
	order := []string{
		"Initial Exec Comments (Please put Dates & Names)", "Investigation Required (Yes/No)?",
		"Responsible Officer?", "Email Sent Date", "Offending Club Response Received? (Yes/No)",
		"Offending Club Response Date?", "Offending Club Response Text", "Ready for Final Decision ",
		"POINTS deduction", "Cards", "Outcome Comms Shared with reporting and offending clubs?",
		"Case Closed? (Yes/No)",
	}
	fmt.Fprint(w, `<details class="mt-3"><summary class="fw-semibold">Manual tracker history (verbatim)</summary><dl class="row mt-3 mb-0">`)
	for _, key := range order {
		value := strings.TrimSpace(row.ManualHistory[key])
		if value == "" {
			continue
		}
		fmt.Fprintf(w, `<dt class="col-lg-4">%s</dt><dd class="col-lg-8" style="white-space:pre-wrap">%s</dd>`, escapeHTML(strings.TrimSpace(key)), escapeHTML(value))
	}
	fmt.Fprint(w, `</dl></details>`)
}

func writeBackfillOption(w io.Writer, value, label, selected string) {
	selection := ""
	if value == selected {
		selection = " selected"
	}
	fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(value), selection, escapeHTML(label))
}

func writeIneligibleBackfillApplication(w io.Writer, csrf, defaultAdmin string, preview ineligibledomain.BackfillApplyPreview, previewErr error, loc *time.Location) {
	fmt.Fprint(w, `<section class="card shadow-sm mt-4"><div class="card-header fw-semibold">Post-sign-off historical application</div><div class="card-body">`)
	if previewErr != nil {
		fmt.Fprintf(w, `<div class="alert alert-danger mb-0">Application readiness could not be checked: %s</div></div></section>`, escapeHTML(previewErr.Error()))
		return
	}
	if preview.AlreadyApplied {
		appliedAt := ""
		if preview.AppliedAt != nil {
			appliedAt = preview.AppliedAt.In(loc).Format("02 Jan 2006 15:04")
		}
		fmt.Fprintf(w, `<div class="alert alert-success"><strong>Applied once and locked.</strong> Application #%d was recorded by %s on %s.</div><p class="mb-0">%d accepted rows were imported as private history: %d restored to investigating and %d closed unpublished. %d unmatched and %d excluded rows remained in reconciliation.</p></div></section>`, preview.ApplicationID, escapeHTML(preview.AppliedByName), escapeHTML(appliedAt), preview.AcceptedRows, preview.OpenRows, preview.ClosedRows, preview.UnmatchedRows, preview.ExcludedRows)
		return
	}
	if preview.SignoffID == 0 {
		fmt.Fprint(w, `<div class="alert alert-secondary mb-0">Complete the named reconciliation sign-off before application readiness is evaluated.</div></div></section>`)
		return
	}
	signedAt := ""
	if preview.SignedOffAt != nil {
		signedAt = preview.SignedOffAt.In(loc).Format("02 Jan 2006 15:04")
	}
	fmt.Fprintf(w, `<p>Using named sign-off #%d by <strong>%s</strong> on %s. Any review recorded after that sign-off blocks this step until a fresh sign-off is completed.</p><div class="row row-cols-2 row-cols-md-5 g-2 mb-3">`, preview.SignoffID, escapeHTML(preview.SignatoryName), escapeHTML(signedAt))
	for _, value := range []struct {
		label string
		count int
	}{{"Accepted", preview.AcceptedRows}, {"Open → investigating", preview.OpenRows}, {"Closed → closed", preview.ClosedRows}, {"Unmatched stay", preview.UnmatchedRows}, {"Excluded stay", preview.ExcludedRows}} {
		fmt.Fprintf(w, `<div class="col"><div class="border rounded p-2 h-100"><strong>%d</strong><div class="small text-muted">%s</div></div></div>`, value.count, escapeHTML(value.label))
	}
	fmt.Fprint(w, `</div>`)
	if len(preview.Rows) > 0 {
		fmt.Fprint(w, `<div class="table-responsive mb-3"><table class="table table-sm align-middle"><thead><tr><th>Tracker row</th><th>Intake</th><th>Linked case</th><th>Current status</th><th>Reviewed target</th></tr></thead><tbody>`)
		for _, row := range preview.Rows {
			caseRef := `<span class="text-danger">Missing or ambiguous</span>`
			currentStatus := "-"
			if row.Case != nil {
				caseRef = fmt.Sprintf(`<a href="/admin/cases/%d">%s</a>`, row.Case.ID, escapeHTML(row.Case.Reference))
				currentStatus = row.Case.Status + " / " + row.Case.PublicStatus
			}
			target := "Investigating (private)"
			if row.ReviewedCaseState == "closed" {
				target = "Closed (unpublished)"
			}
			fmt.Fprintf(w, `<tr><td>%d</td><td><a href="/admin/ineligible/%d">%d</a></td><td>%s</td><td>%s</td><td>%s</td></tr>`, row.SourceRowNumber, row.IntakeID, row.IntakeID, caseRef, escapeHTML(currentStatus), escapeHTML(target))
		}
		fmt.Fprint(w, `</tbody></table></div>`)
	}
	if len(preview.Issues) > 0 {
		fmt.Fprint(w, `<div class="alert alert-danger"><strong>Application is blocked.</strong><ul class="mb-0 mt-2">`)
		for _, issue := range preview.Issues {
			fmt.Fprintf(w, `<li>%s</li>`, escapeHTML(issue))
		}
		fmt.Fprint(w, `</ul></div></div></section>`)
		return
	}
	if !preview.Ready() {
		fmt.Fprint(w, `<div class="alert alert-warning mb-0">This reconciliation is not ready for application.</div></div></section>`)
		return
	}
	fmt.Fprintf(w, `<div class="alert alert-warning"><strong>Non-operative historical application.</strong> This action can append private history, restore reviewed status, and retain closed-row outcome snapshots. It cannot create a live decision, sanction effect, card/points ledger entry, follow-up task, correspondence or outbox message. Safety counts are checked before and after in the same transaction.</div><form method="POST" action="/admin/ineligible/backfill/%d/apply"><input type="hidden" name="csrf_token" value="%s"><div class="row g-3"><div class="col-md-4"><label class="form-label">Applying administrator</label><input class="form-control" value="%s" readonly></div><div class="col-md-8"><label class="form-label">Application note</label><textarea class="form-control" name="application_note" rows="2" maxlength="5000" required>Apply the signed-off 2026 tracker history and reviewed open/closed state. Historical points/cards prose remains non-operative.</textarea></div><div class="col-12"><label class="form-check"><input class="form-check-input" type="checkbox" name="confirm_apply" value="yes" required> <span class="form-check-label">I confirm this one-time, idempotent application. Unmatched/excluded rows remain untouched and no historical email will be sent.</span></label></div></div><button class="btn btn-danger mt-3">Apply private history and reviewed status</button></form></div></section>`, preview.RunID, escapeHTML(csrf), escapeHTML(defaultAdmin))
}

func (s *Server) handleAdminIneligibleBackfillApply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, ok := positiveBackfillParam(r, "runID")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil || r.FormValue("confirm_apply") != "yes" {
			redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "error", "Confirm the one-time historical application.")
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		appliedByName := strings.TrimSpace(actor.Label)
		if appliedByName == "" {
			appliedByName = fmt.Sprintf("admin #%d", *actor.ID)
		}
		note := strings.TrimSpace(r.FormValue("application_note"))
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		summary, err := ineligibledomain.ApplyTrackerBackfill(ctx, s.DB, runID, int64(*actor.ID), appliedByName, note)
		if err != nil {
			redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "error", "Application blocked: "+truncateBackfillMessage(err.Error(), 1000))
			return
		}
		if !summary.AlreadyApplied {
			applicationID := summary.ApplicationID
			s.audit(ctx, r, "admin", actor.ID, "ineligible_tracker_backfill_applied", "sanction_ineligible_backfill_application", &applicationID, map[string]any{"run_id": runID, "accepted_rows": summary.AcceptedRows, "open_rows": summary.OpenRows, "closed_rows": summary.ClosedRows, "unmatched_rows": summary.UnmatchedRows, "excluded_rows": summary.ExcludedRows})
		}
		message := fmt.Sprintf("Application %d complete: %d private history rows, %d investigating and %d closed unpublished with non-operative outcome snapshots; no effect, ledger or message was created.", summary.ApplicationID, summary.AcceptedRows, summary.OpenRows, summary.ClosedRows)
		if summary.AlreadyApplied {
			message = fmt.Sprintf("Application %d was already complete; the retry made no changes.", summary.ApplicationID)
		}
		redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "success", message)
	}
}

func (s *Server) handleAdminIneligibleBackfillReview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, runOK := positiveBackfillParam(r, "runID")
		rowID, rowOK := positiveBackfillParam(r, "rowID")
		if !runOK || !rowOK {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "error", "Invalid review form.")
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		intakeID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("intake_id")), 10, 64)
		input := ineligibledomain.BackfillReviewInput{
			Disposition:          strings.TrimSpace(r.FormValue("disposition")),
			ReviewedIntakeID:     intakeID,
			ReviewedCaseState:    strings.TrimSpace(r.FormValue("case_state")),
			EffectsReviewStatus:  strings.TrimSpace(r.FormValue("effects_status")),
			EffectInterpretation: strings.TrimSpace(r.FormValue("effect_interpretation")),
			ReviewReason:         strings.TrimSpace(r.FormValue("review_reason")),
			ReviewerName:         strings.TrimSpace(r.FormValue("reviewer_name")),
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "could not start tracker review", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('gmcl_ineligible_tracker_backfill_review'))`); err != nil {
			http.Error(w, "could not lock tracker review", http.StatusInternalServerError)
			return
		}
		var requiresEffectReview bool
		if err = tx.QueryRow(ctx, `SELECT requires_effect_review FROM sanction_ineligible_backfill_rows WHERE id=$1 AND run_id=$2`, rowID, runID).Scan(&requiresEffectReview); err != nil {
			http.NotFound(w, r)
			return
		}
		if err = ineligibledomain.ValidateBackfillReview(requiresEffectReview, input); err != nil {
			redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "error", err.Error())
			return
		}
		var reviewedIntake any
		if input.Disposition == "accept_match" {
			var valid bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sanction_intakes WHERE id=$1 AND origin='google_form')`, input.ReviewedIntakeID).Scan(&valid); err != nil || !valid {
				redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "error", "The accepted intake ID is not a staged Google-form response.")
				return
			}
			var alreadyUsed bool
			if err = tx.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM sanction_ineligible_backfill_rows other
					JOIN LATERAL (
						SELECT disposition,reviewed_intake_id
						FROM sanction_ineligible_backfill_reviews rv
						WHERE rv.backfill_row_id=other.id ORDER BY rv.id DESC LIMIT 1
					) latest ON TRUE
					WHERE other.run_id=$1 AND other.id<>$2
					  AND latest.disposition='accept_match' AND latest.reviewed_intake_id=$3
				)
			`, runID, rowID, input.ReviewedIntakeID).Scan(&alreadyUsed); err != nil {
				http.Error(w, "could not validate tracker intake uniqueness", http.StatusInternalServerError)
				return
			}
			if alreadyUsed {
				redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "error", "That intake is already the accepted match for another row in this run.")
				return
			}
			reviewedIntake = input.ReviewedIntakeID
		}
		var supersedes any
		var previousID int64
		if err = tx.QueryRow(ctx, `SELECT id FROM sanction_ineligible_backfill_reviews WHERE backfill_row_id=$1 ORDER BY id DESC LIMIT 1`, rowID).Scan(&previousID); err == nil {
			supersedes = previousID
		} else if err != pgx.ErrNoRows {
			http.Error(w, "could not inspect prior tracker review", http.StatusInternalServerError)
			return
		}
		var reviewID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO sanction_ineligible_backfill_reviews(
				backfill_row_id,supersedes_id,disposition,reviewed_intake_id,
				reviewed_case_state,effects_review_status,effect_interpretation,
				review_reason,reviewed_by_admin_id,reviewed_by_name
			) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10) RETURNING id
		`, rowID, supersedes, input.Disposition, reviewedIntake, input.ReviewedCaseState,
			input.EffectsReviewStatus, input.EffectInterpretation, input.ReviewReason,
			*actor.ID, input.ReviewerName).Scan(&reviewID)
		if err != nil {
			http.Error(w, "could not record tracker review", http.StatusInternalServerError)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			http.Error(w, "could not commit tracker review", http.StatusInternalServerError)
			return
		}
		s.audit(ctx, r, "admin", actor.ID, "ineligible_tracker_row_reviewed", "sanction_ineligible_backfill_review", &reviewID, map[string]any{"run_id": runID, "row_id": rowID, "disposition": input.Disposition, "case_state": input.ReviewedCaseState, "effects_status": input.EffectsReviewStatus})
		redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "success", fmt.Sprintf("Row verified and moved to Verified history (record %d).", reviewID))
	}
}

func (s *Server) loadIneligibleBackfillReadiness(ctx context.Context, runID int64) (ineligibledomain.BackfillSignoffReadiness, error) {
	return loadIneligibleBackfillReadinessFrom(ctx, s.DB, runID)
}

type ineligibleBackfillQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadIneligibleBackfillReadinessFrom(ctx context.Context, query ineligibleBackfillQueryRower, runID int64) (ineligibledomain.BackfillSignoffReadiness, error) {
	var readiness ineligibledomain.BackfillSignoffReadiness
	err := query.QueryRow(ctx, `
		WITH staged AS (
			SELECT id,requires_effect_review FROM sanction_ineligible_backfill_rows WHERE run_id=$1
		), latest AS (
			SELECT s.id AS row_id,s.requires_effect_review,review.id,review.disposition,
			       review.reviewed_case_state,review.effects_review_status
			FROM staged s
			LEFT JOIN LATERAL (
				SELECT id,disposition,reviewed_case_state,effects_review_status
				FROM sanction_ineligible_backfill_reviews rv
				WHERE rv.backfill_row_id=s.id ORDER BY rv.id DESC LIMIT 1
			) review ON TRUE
		)
		SELECT COUNT(*),COUNT(id),COUNT(*) FILTER(WHERE disposition='exclude_tracker_row'),
		       COUNT(*) FILTER(WHERE disposition<>'exclude_tracker_row' AND reviewed_case_state NOT IN ('open','closed')),
		       COUNT(*) FILTER(WHERE disposition<>'exclude_tracker_row' AND requires_effect_review
		                         AND effects_review_status NOT IN ('manually_interpreted','confirmed_no_effect'))
		FROM latest
	`, runID).Scan(&readiness.RowsTotal, &readiness.RowsReviewed, &readiness.RowsExcluded,
		&readiness.RowsNeedingStateReview, &readiness.RowsNeedingEffectReview)
	return readiness, err
}

func (s *Server) handleAdminIneligibleBackfillSignoff() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, ok := positiveBackfillParam(r, "runID")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil || r.FormValue("confirm") != "yes" {
			redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "error", "Tick the audit-history confirmation before verifying the import.")
			return
		}
		signatory := strings.TrimSpace(r.FormValue("signatory_name"))
		statement := strings.TrimSpace(r.FormValue("statement"))
		if signatory == "" || statement == "" || len(signatory) > 200 || len(statement) > 2000 {
			redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "error", "A valid signatory name and statement are required.")
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "could not start tracker sign-off", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('gmcl_ineligible_tracker_backfill_review'))`); err != nil {
			http.Error(w, "could not lock tracker sign-off", http.StatusInternalServerError)
			return
		}
		readiness, err := loadIneligibleBackfillReadinessFrom(ctx, tx, runID)
		if err != nil {
			http.Error(w, "could not verify tracker readiness", http.StatusInternalServerError)
			return
		}
		if err = readiness.Validate(); err != nil {
			redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "error", err.Error())
			return
		}
		var reviewSnapshot string
		if err = tx.QueryRow(ctx, `
			SELECT COALESCE(
				jsonb_agg(
					jsonb_build_object('row_id',staged.id,'review_id',review.id)
					ORDER BY staged.source_row_number
				),
				'[]'::jsonb
			)::text
			FROM sanction_ineligible_backfill_rows staged
			JOIN LATERAL (
				SELECT id FROM sanction_ineligible_backfill_reviews rv
				WHERE rv.backfill_row_id=staged.id ORDER BY rv.id DESC LIMIT 1
			) review ON TRUE
			WHERE staged.run_id=$1
		`, runID).Scan(&reviewSnapshot); err != nil {
			http.Error(w, "could not snapshot signed tracker reviews", http.StatusInternalServerError)
			return
		}
		totals, _ := json.Marshal(readiness)
		var signoffID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO sanction_ineligible_backfill_signoffs(
				run_id,signatory_name,signoff_statement,reconciliation_totals,
				review_snapshot,signed_off_by_admin_id
			) SELECT $1,$2,$3,$4::jsonb,$5::jsonb,$6
			  WHERE EXISTS(SELECT 1 FROM sanction_ineligible_backfill_runs WHERE id=$1)
			RETURNING id
		`, runID, signatory, statement, string(totals), reviewSnapshot, *actor.ID).Scan(&signoffID)
		if err != nil {
			http.Error(w, "could not record tracker sign-off", http.StatusInternalServerError)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			http.Error(w, "could not commit tracker sign-off", http.StatusInternalServerError)
			return
		}
		s.audit(ctx, r, "admin", actor.ID, "ineligible_tracker_reconciliation_signed_off", "sanction_ineligible_backfill_signoff", &signoffID, map[string]any{"run_id": runID, "signatory": signatory, "totals": readiness})
		redirectIneligibleBackfill(w, r, fmt.Sprintf("/admin/ineligible/backfill/%d", runID), "success", fmt.Sprintf("Reconciliation signed off by %s (record %d).", signatory, signoffID))
	}
}

func (s *Server) handleAdminIneligibleBackfillSource() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, ok := positiveBackfillParam(r, "runID")
		if !ok {
			http.NotFound(w, r)
			return
		}
		var filename, storageKey, expectedSHA string
		var byteSize int64
		if err := s.DB.QueryRow(r.Context(), `SELECT source_filename,storage_key,source_sha256,byte_size FROM sanction_ineligible_backfill_runs WHERE id=$1`, runID).Scan(&filename, &storageKey, &expectedSHA, &byteSize); err != nil {
			http.NotFound(w, r)
			return
		}
		if storageKey != expectedSHA+".xlsx" || filepath.Base(storageKey) != storageKey || byteSize <= 0 || byteSize > maxIneligibleBackfillUpload {
			http.Error(w, "stored tracker provenance is invalid", http.StatusInternalServerError)
			return
		}
		data, err := os.ReadFile(filepath.Join(ineligibledomain.BackfillStorageDir(), storageKey))
		if err != nil || int64(len(data)) != byteSize {
			http.Error(w, "stored tracker source is unavailable", http.StatusInternalServerError)
			return
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != expectedSHA {
			http.Error(w, "stored tracker source failed checksum verification", http.StatusInternalServerError)
			return
		}
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(filename)})
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", disposition)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-SHA256", expectedSHA)
		_, _ = w.Write(data)
	}
}

func positiveBackfillParam(r *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return value, err == nil && value > 0
}

func redirectIneligibleBackfill(w http.ResponseWriter, r *http.Request, target, key, message string) {
	separator := "?"
	if strings.Contains(target, "?") {
		separator = "&"
	}
	http.Redirect(w, r, target+separator+key+"="+urlQueryEscape(message), http.StatusSeeOther)
}

func writeIneligibleBackfillFlash(w io.Writer, r *http.Request) {
	if message := strings.TrimSpace(r.URL.Query().Get("success")); message != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(message))
	}
	if message := strings.TrimSpace(r.URL.Query().Get("error")); message != "" {
		fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(message))
	}
}

func shortHash(value string) string {
	if len(value) <= 16 {
		return value
	}
	return value[:16]
}

func truncateBackfillMessage(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func valueIfPositive(value int64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func backfillDisabledIf(disabled bool) string {
	if disabled {
		return " disabled"
	}
	return ""
}
