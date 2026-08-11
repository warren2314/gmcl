package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	ineligibledomain "cricket-ground-feedback/internal/ineligible"
	"cricket-ground-feedback/internal/middleware"
)

func (s *Server) handleAdminIneligibleSelection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("run_id")), 10, 64)
		if strings.TrimSpace(r.URL.Query().Get("run_id")) == "" {
			runID = 0
			err = nil
		}
		if err != nil || runID < 0 {
			http.Error(w, "invalid import run", http.StatusBadRequest)
			return
		}
		run, err := ineligibledomain.LoadWorklistRun(r.Context(), s.DB, runID)
		if err != nil {
			if errors.Is(err, ineligibledomain.ErrWorklistRunNotReady) || errors.Is(err, ineligibledomain.ErrWorklistSelectionStale) {
				redirectIneligibleDashboard(w, r, "error", "Run Import and choose reports before selecting a work list.")
				return
			}
			http.Error(w, "could not load imported reports", http.StatusInternalServerError)
			return
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Choose ineligible-player reports")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 py-4"><div class="d-flex flex-column flex-lg-row justify-content-between gap-3 mb-4"><div><h1 class="h2 mb-1">Choose the reports to progress</h1><p class="text-muted mb-0">The system reads spreadsheet values, not cell colours. Tick the reports marked for action in the supplied tracker.</p></div><a class="btn btn-outline-secondary align-self-lg-start" href="/admin/ineligible">Cancel - keep current queue</a></div>`)
		writeIneligibleFlash(w, r)
		fmt.Fprintf(w, `<section class="alert alert-info"><strong>Import %d:</strong> %d rows seen, %d added, %d changed and %d errors.<br><span class="small">Unselected reports will be hidden from the normal queue, not deleted. They can be restored here later.</span></section>`, run.RunID, run.RowsSeen, run.RowsNew, run.RowsChanged, run.RowsErrored)
		if run.ManifestCount != run.RowsSeen {
			fmt.Fprintf(w, `<div class="alert alert-danger"><strong>Selection is blocked.</strong> The import recorded %d of %d source rows. Run the import again after checking the sync error.</div>`, run.ManifestCount, run.RowsSeen)
		}
		if len(run.UnresolvedRows) > 0 {
			fmt.Fprintf(w, `<details class="alert alert-warning" open><summary class="fw-semibold">%d source rows need identity help</summary><ul class="mb-0 mt-2">`, len(run.UnresolvedRows))
			for _, item := range run.UnresolvedRows {
				fmt.Fprintf(w, `<li>Spreadsheet row %d: %s</li>`, item.SourceRowNumber, escapeHTML(item.Error))
			}
			fmt.Fprint(w, `</ul><div class="small mt-2"><strong>Selection is blocked</strong> until a fresh import can match every source row safely.</div></details>`)
		}
		if len(run.Candidates) == 0 {
			fmt.Fprint(w, `<div class="alert alert-secondary">This import has no unresolved, unlinked Google reports to select.</div></main>`)
			pageFooter(w)
			return
		}
		disabled := ""
		if !run.Ready() {
			disabled = " disabled"
		}
		if !run.Ready() && run.ManifestCount == run.RowsSeen && len(run.UnresolvedRows) == 0 {
			fmt.Fprint(w, `<div class="alert alert-danger"><strong>Selection is blocked.</strong> This import has no current reports that can be selected safely. Run the import again.</div>`)
		}
		fmt.Fprintf(w, `<form method="POST" action="/admin/ineligible/selection" id="ineligible-selection-form"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="run_id" value="%d"><input type="hidden" name="base_batch_id" value="%d"><input type="hidden" name="candidate_sha256" value="%s"><section class="card shadow-sm mb-3"><div class="card-header"><div class="row g-2 align-items-center"><div class="col-lg"><strong>Imported reports</strong><div class="small text-muted"><span id="selection-count">0</span> selected from %d reports</div></div><div class="col-lg-5"><label class="visually-hidden" for="selection-search">Search reports</label><input class="form-control" id="selection-search" type="search" placeholder="Search player, club, team or date"></div><div class="col-auto"><button class="btn btn-sm btn-outline-primary" type="button" id="selection-select-shown">Select all shown</button></div><div class="col-auto"><button class="btn btn-sm btn-outline-secondary" type="button" id="selection-clear">Clear selection</button></div></div></div><div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0" id="selection-table"><thead><tr><th scope="col">Progress</th><th scope="col">Report</th><th scope="col">Fixture</th><th scope="col">Received</th><th scope="col">Current status</th></tr></thead><tbody>`, escapeHTML(csrf), run.RunID, run.CurrentBatchID, escapeHTML(run.CandidateSHA256), len(run.Candidates))
		for _, item := range run.Candidates {
			checked := ""
			if run.CurrentBatchID > 0 && item.VisibilityBatchID > 0 && item.CurrentVisibility == "visible" && item.Selectable {
				checked = " checked"
			}
			checkboxDisabled := ""
			selectionHelp := ""
			if !item.Selectable {
				checkboxDisabled = " disabled"
				selectionHelp = `<div class="small text-warning-emphasis">Not confirmed in this import; it will remain off the selected list.</div>`
			}
			fixture := "-"
			if item.FixtureDate != nil {
				fixture = item.FixtureDate.Format("02 Jan 2006")
			}
			received := "-"
			if item.ReceivedAt != nil {
				received = item.ReceivedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")
			}
			evidence := ""
			if item.EvidenceCount > 0 {
				evidenceLabel := "files"
				if item.EvidenceCount == 1 {
					evidenceLabel = "file"
				}
				evidence = fmt.Sprintf(`<div class="small"><span class="badge text-bg-light border">%d evidence %s</span></div>`, item.EvidenceCount, evidenceLabel)
			}
			worklistStatus := plainIneligibleWorklist(item.CurrentVisibility)
			if item.VisibilityBatchID == 0 {
				worklistStatus = "Not yet chosen"
			}
			fmt.Fprintf(w, `<tr data-selection-row><td data-label="Progress"><input class="form-check-input" type="checkbox" name="selected_intake_id" value="%d" aria-label="Progress %s"%s%s></td><td data-label="Report"><a href="/admin/ineligible/%d"><strong>%s</strong></a><div class="small text-muted">%s / %s</div><div class="small text-muted">Reported by %s; spreadsheet row %d</div>%s%s</td><td data-label="Fixture">%s</td><td data-label="Received">%s</td><td data-label="Current status">%s<div class="small text-muted mt-1">%s</div></td></tr>`, item.IntakeID, escapeHTML(item.Player), checked, checkboxDisabled, item.IntakeID, escapeHTML(item.Player), escapeHTML(item.OffendingClub), escapeHTML(item.Team), escapeHTML(item.ReportingClub), item.SourceRowNumber, evidence, selectionHelp, escapeHTML(fixture), escapeHTML(received), ineligibleStateBadge(item.State), escapeHTML(worklistStatus))
		}
		fmt.Fprintf(w, `</tbody></table></div></section><section class="card border-primary"><div class="card-body"><label class="form-label fw-semibold" for="selection-reason">Handover label</label><input class="form-control" id="selection-reason" name="reason" required minlength="3" maxlength="200" placeholder="For example: Rev 8 blue rows"><div class="form-text">Saved with your name, the exact import and every selected or deferred report.</div></div><div class="card-footer d-flex flex-column flex-sm-row justify-content-between gap-2"><span class="small text-muted align-self-sm-center">This action creates no cases, emails or sanctions.</span><button class="btn btn-primary"%s>Save selection and show work queue</button></div></section></form>`, disabled)
		fmt.Fprint(w, `<script>(function(){var search=document.getElementById('selection-search');var rows=Array.prototype.slice.call(document.querySelectorAll('[data-selection-row]'));var count=document.getElementById('selection-count');function boxes(){return rows.map(function(row){return row.querySelector('input[type="checkbox"]');}).filter(Boolean);}function update(){count.textContent=boxes().filter(function(box){return box.checked;}).length;}search.addEventListener('input',function(){var q=search.value.toLowerCase().trim();rows.forEach(function(row){row.hidden=q!==''&&row.textContent.toLowerCase().indexOf(q)===-1;});});document.getElementById('selection-select-shown').addEventListener('click',function(){rows.forEach(function(row){var box=row.querySelector('input[type="checkbox"]');if(!row.hidden&&box&&!box.disabled){box.checked=true;}});update();});document.getElementById('selection-clear').addEventListener('click',function(){boxes().forEach(function(box){if(!box.disabled){box.checked=false;}});update();});boxes().forEach(function(box){box.addEventListener('change',update);});update();}());</script></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminIneligibleSelectionPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid selection form", http.StatusBadRequest)
			return
		}
		runID, runErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("run_id")), 10, 64)
		baseBatchID, batchErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("base_batch_id")), 10, 64)
		selectedIDs, selectedErr := parseIneligibleSelectionIDs(r.Form["selected_intake_id"])
		if runErr != nil || batchErr != nil || runID < 1 || baseBatchID < 0 {
			http.Error(w, "invalid report selection", http.StatusBadRequest)
			return
		}
		if len(r.Form["selected_intake_id"]) == 0 {
			values := url.Values{"run_id": []string{strconv.FormatInt(runID, 10)}, "error": []string{"Select at least one confirmed report and enter a handover label."}}
			http.Redirect(w, r, "/admin/ineligible/selection?"+values.Encode(), http.StatusSeeOther)
			return
		}
		if selectedErr != nil {
			http.Error(w, "invalid report selection", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		result, err := ineligibledomain.ApplyWorklistSelection(r.Context(), s.DB, ineligibledomain.WorklistSelectionInput{
			RunID: runID, BaseBatchID: baseBatchID,
			CandidateSHA256: strings.TrimSpace(r.FormValue("candidate_sha256")),
			SelectedIDs:     selectedIDs, Reason: r.FormValue("reason"),
			AdminID: *actor.ID, AdminLabel: actor.Label, RequestID: actor.RequestID,
		})
		if err != nil {
			message := "The work-list selection could not be saved."
			switch {
			case errors.Is(err, ineligibledomain.ErrWorklistSelectionStale):
				message = "The imported reports or work list changed while this page was open. Review the refreshed list before saving."
			case errors.Is(err, ineligibledomain.ErrWorklistRunNotReady):
				message = "This import is incomplete and cannot be used for selection. Run the import again after resolving its error."
			case errors.Is(err, ineligibledomain.ErrWorklistSelectionInvalid):
				message = "Select at least one confirmed report and enter a handover label."
			default:
				http.Error(w, "could not save report selection", http.StatusInternalServerError)
				return
			}
			values := url.Values{"run_id": []string{strconv.FormatInt(runID, 10)}, "error": []string{message}}
			http.Redirect(w, r, "/admin/ineligible/selection?"+values.Encode(), http.StatusSeeOther)
			return
		}
		message := fmt.Sprintf("Work list saved: %d reports selected and %d hidden; no reports were deleted and no emails were sent.", result.SelectedCount, result.DeferredCount)
		if result.Unchanged {
			message = fmt.Sprintf("Work list unchanged: %d reports selected and %d hidden.", result.SelectedCount, result.DeferredCount)
		}
		values := url.Values{"success": []string{message}}
		http.Redirect(w, r, "/admin/ineligible?"+values.Encode(), http.StatusSeeOther)
	}
}

func parseIneligibleSelectionIDs(values []string) ([]int64, error) {
	if len(values) == 0 || len(values) > ineligibledomain.MaxWorklistCandidates {
		return nil, ineligibledomain.ErrWorklistSelectionInvalid
	}
	result := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || id < 1 {
			return nil, ineligibledomain.ErrWorklistSelectionInvalid
		}
		if _, exists := seen[id]; exists {
			return nil, ineligibledomain.ErrWorklistSelectionInvalid
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}
