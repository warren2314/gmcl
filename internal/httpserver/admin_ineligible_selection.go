package httpserver

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 py-4"><div class="d-flex flex-column flex-lg-row justify-content-between gap-3 mb-4"><div><h1 class="h2 mb-1">Choose the reports to progress</h1><p class="text-muted mb-0">The import reads every row in the configured Google response sheet. This page shows open reports that have not already been linked or resolved. Cell colours are not imported.</p></div><a class="btn btn-outline-secondary align-self-lg-start" href="/admin/ineligible">Cancel - keep current queue</a></div>`)
		writeIneligibleFlash(w, r)
		summary := summarizeIneligibleSelectionRun(run)
		fmt.Fprintf(w, `<section class="alert alert-info"><strong>Import %d:</strong> %d source rows read, %d added, %d changed and %d need attention.<div class="small mt-1"><strong>%d open reports confirmed by this import are available to select.</strong> New and updated reports are shown first. %d source rows were already linked, resolved, ignored or marked duplicate.`, run.RunID, run.RowsSeen, run.RowsNew, run.RowsChanged, run.RowsErrored, summary.Selectable, summary.AlreadyHandled)
		if summary.ObservedNotCurrent > 0 {
			fmt.Fprintf(w, ` %d open reports changed after this import and are shown disabled.`, summary.ObservedNotCurrent)
		}
		if summary.OlderOpen > 0 {
			fmt.Fprintf(w, ` %d older open reports not confirmed by this import are also shown disabled.`, summary.OlderOpen)
		}
		fmt.Fprint(w, `</div><div class="small mt-1"><strong>Rows read is not the number available below.</strong> Zero added or changed is normal when the same sheet is imported again. One spreadsheet row remains one report, even if its Player box contains several names.</div><div class="small mt-2"><a href="/admin/ineligible?scope=all&amp;state=all&amp;worklist=all&amp;sort=fixture_newest#reports">Open report history</a> to find a report that has already been progressed. Unselected open reports will be hidden from the normal queue, not deleted.</div></section>`)
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
			fmt.Fprint(w, `<div class="alert alert-secondary">No open, unlinked reports are available from this import. The source rows may already be linked or resolved; check <a href="/admin/ineligible?scope=all&amp;state=all&amp;worklist=all&amp;sort=fixture_newest#reports">report history</a>.</div></main>`)
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
		fmt.Fprintf(w, `<form method="POST" action="/admin/ineligible/selection" id="ineligible-selection-form"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="run_id" value="%d"><input type="hidden" name="base_batch_id" value="%d"><input type="hidden" name="candidate_sha256" value="%s"><section class="card shadow-sm mb-3">`, escapeHTML(csrf), run.RunID, run.CurrentBatchID, escapeHTML(run.CandidateSHA256))
		writeIneligibleSelectionControls(w, len(run.Candidates), summary.Selectable)
		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0" id="selection-table"><thead><tr><th scope="col">Progress</th><th scope="col">Report</th><th scope="col">Fixture</th><th scope="col">Received</th><th scope="col">Current status</th></tr></thead><tbody>`)
		for sourceOrder, item := range run.Candidates {
			checked := ""
			if run.CurrentBatchID > 0 && item.VisibilityBatchID > 0 && item.CurrentVisibility == "visible" && item.Selectable {
				checked = " checked"
			}
			checkboxDisabled := ""
			selectionHelp := ""
			if !item.Selectable {
				checkboxDisabled = " disabled"
				selectionHelp = fmt.Sprintf(`<div class="small text-warning-emphasis">Not confirmed by import %d, so it cannot be selected from this run.</div>`, run.RunID)
			}
			if item.ExceptionMessage != "" {
				selectionHelp += `<details class="small text-warning-emphasis"><summary><strong>Needs attention</strong> - show details</summary><div class="mt-1">` + escapeHTML(item.ExceptionMessage) + `</div></details>`
			}
			fixture, fixtureISO := ineligibleSelectionFixtureDate(item.FixtureDate)
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
			importStatus := ineligibleImportDispositionBadge(item.ImportDisposition)
			fmt.Fprintf(w, `<tr data-selection-row data-fixture-date="%s" data-original-order="%d"><td data-label="Progress"><input class="form-check-input" type="checkbox" name="selected_intake_id" value="%d" aria-label="Progress %s"%s%s></td><td data-label="Report"><a href="/admin/ineligible/%d"><strong>%s</strong></a><div class="small text-muted">%s / %s</div><div class="small text-muted">Reported by %s; spreadsheet row %d</div>%s%s</td><td data-label="Fixture">%s</td><td data-label="Received">%s</td><td data-label="Current status">%s %s<div class="small text-muted mt-1">%s</div></td></tr>`, escapeHTML(fixtureISO), sourceOrder, item.IntakeID, escapeHTML(item.Player), checked, checkboxDisabled, item.IntakeID, escapeHTML(item.Player), escapeHTML(item.OffendingClub), escapeHTML(item.Team), escapeHTML(item.ReportingClub), item.SourceRowNumber, evidence, selectionHelp, escapeHTML(fixture), escapeHTML(received), ineligibleStateBadge(item.State), importStatus, escapeHTML(worklistStatus))
		}
		fmt.Fprintf(w, `</tbody></table></div></section><section class="card border-primary"><div class="card-body"><label class="form-label fw-semibold" for="selection-reason">Handover label</label><input class="form-control" id="selection-reason" name="reason" required minlength="3" maxlength="200" placeholder="For example: Dave handover - 11 Aug 2026"><div class="form-text">Saved with your name, the exact import and every selected or deferred report.</div></div><div class="card-footer d-flex flex-column flex-sm-row justify-content-between gap-2"><span class="small text-muted align-self-sm-center">This action creates no cases, emails or sanctions.</span><button class="btn btn-primary"%s>Save selection and show work queue</button></div></section></form>`, disabled)
		writeIneligibleSelectionScript(w)
		fmt.Fprint(w, `</main>`)
		pageFooter(w)
	}
}

func ineligibleImportDispositionBadge(disposition string) string {
	labels := map[string]string{
		"new":        "New in this import",
		"changed":    "Updated in this import",
		"exception":  "Needs attention",
		"unchanged":  "Previously imported",
		"older_open": "Older open report",
	}
	label := labels[disposition]
	if label == "" {
		label = "Previously imported"
	}
	return `<span class="badge text-bg-light border">` + escapeHTML(label) + `</span>`
}

func ineligibleSelectionFixtureDate(value *time.Time) (string, string) {
	if value == nil {
		return "-", ""
	}
	return value.Format("02 Jan 2006"), value.Format("2006-01-02")
}

func writeIneligibleSelectionControls(w io.Writer, total, selectable int) {
	fmt.Fprintf(w, `<div class="card-header"><div class="row g-2 align-items-center"><div class="col-lg"><strong>Open reports</strong><div class="small text-muted" aria-live="polite"><span id="selection-shown-count">%d</span> shown &middot; <span id="selection-count">0</span> selected &middot; %d currently available to select</div><div class="small text-muted">Filtering only changes what is shown. Reports already ticked stay selected. Select all shown chooses only visible, enabled rows.</div></div><div class="col-lg-5"><label class="visually-hidden" for="selection-search">Search reports</label><input class="form-control" id="selection-search" type="search" placeholder="Search player, club, team or date"></div></div><div class="row g-2 align-items-end mt-2"><div class="col-6 col-md-3 col-lg-2"><label class="form-label" for="selection-fixture-from">Fixture from</label><input class="form-control" id="selection-fixture-from" type="date"></div><div class="col-6 col-md-3 col-lg-2"><label class="form-label" for="selection-fixture-to">Fixture to</label><input class="form-control" id="selection-fixture-to" type="date"></div><div class="col-12 col-md-4 col-lg-3"><label class="form-label" for="selection-fixture-order">Order</label><select class="form-select" id="selection-fixture-order"><option value="source">Spreadsheet order</option><option value="fixture_newest">Newest fixture first</option><option value="fixture_oldest">Oldest fixture first</option></select></div><div class="col-auto"><button class="btn btn-sm btn-outline-secondary" type="button" id="selection-clear-dates">Clear dates</button></div><div class="col-auto ms-lg-auto"><button class="btn btn-sm btn-outline-primary" type="button" id="selection-select-shown">Select all shown</button></div><div class="col-auto"><button class="btn btn-sm btn-outline-secondary" type="button" id="selection-clear">Clear all selections</button></div></div><div class="alert alert-secondary py-2 mt-3 mb-0" id="selection-no-results" hidden>No reports match this search or fixture range. Clear the dates or search to see more.</div></div>`, total, selectable)
}

type ineligibleSelectionRunSummary struct {
	Selectable         int
	AlreadyHandled     int
	ObservedNotCurrent int
	OlderOpen          int
}

func summarizeIneligibleSelectionRun(run ineligibledomain.WorklistRun) ineligibleSelectionRunSummary {
	var summary ineligibleSelectionRunSummary
	observedCandidates := 0
	for _, item := range run.Candidates {
		if item.ManifestRowID > 0 {
			observedCandidates++
			if item.Selectable {
				summary.Selectable++
			} else {
				summary.ObservedNotCurrent++
			}
		} else {
			summary.OlderOpen++
		}
	}
	resolvedRows := run.ManifestCount - len(run.UnresolvedRows)
	summary.AlreadyHandled = resolvedRows - observedCandidates
	if summary.AlreadyHandled < 0 {
		summary.AlreadyHandled = 0
	}
	return summary
}

func writeIneligibleSelectionScript(w io.Writer) {
	fmt.Fprint(w, `<script>(function(){"use strict";var search=document.getElementById("selection-search");var fixtureFrom=document.getElementById("selection-fixture-from");var fixtureTo=document.getElementById("selection-fixture-to");var fixtureOrder=document.getElementById("selection-fixture-order");var rows=Array.prototype.slice.call(document.querySelectorAll("[data-selection-row]"));var tbody=document.querySelector("#selection-table tbody");var selectionCount=document.getElementById("selection-count");var shownCount=document.getElementById("selection-shown-count");var noResults=document.getElementById("selection-no-results");function boxes(){return rows.map(function(row){return row.querySelector('input[type="checkbox"]');}).filter(Boolean);}function originalOrder(row){return parseInt(row.getAttribute("data-original-order"),10)||0;}function fixtureDate(row){return row.getAttribute("data-fixture-date")||"";}function sortRows(){var order=fixtureOrder.value;rows.slice().sort(function(a,b){if(order==="source"){return originalOrder(a)-originalOrder(b);}var aDate=fixtureDate(a);var bDate=fixtureDate(b);if(aDate===""&&bDate===""){return originalOrder(a)-originalOrder(b);}if(aDate===""){return 1;}if(bDate===""){return -1;}var comparison=aDate<bDate?-1:(aDate>bDate?1:0);if(order==="fixture_newest"){comparison=-comparison;}return comparison||originalOrder(a)-originalOrder(b);}).forEach(function(row){tbody.appendChild(row);});}function applyView(){var query=search.value.toLowerCase().trim();var from=fixtureFrom.value;var to=fixtureTo.value;if(from!==""&&to!==""&&from>to){var swap=from;from=to;to=swap;fixtureFrom.value=from;fixtureTo.value=to;}var hasRange=from!==""||to!=="";var shown=0;rows.forEach(function(row){var date=fixtureDate(row);var matchesSearch=query===""||row.textContent.toLowerCase().indexOf(query)!==-1;var matchesDate=!hasRange||(date!==""&&(from===""||date>=from)&&(to===""||date<=to));row.hidden=!(matchesSearch&&matchesDate);if(!row.hidden){shown+=1;}});shownCount.textContent=shown;noResults.hidden=shown!==0;}function updateSelectionCount(){selectionCount.textContent=boxes().filter(function(box){return box.checked;}).length;}function refreshView(){sortRows();applyView();}search.addEventListener("input",applyView);fixtureFrom.addEventListener("input",applyView);fixtureTo.addEventListener("input",applyView);fixtureOrder.addEventListener("change",refreshView);document.getElementById("selection-clear-dates").addEventListener("click",function(){fixtureFrom.value="";fixtureTo.value="";applyView();});document.getElementById("selection-select-shown").addEventListener("click",function(){rows.forEach(function(row){var box=row.querySelector('input[type="checkbox"]');if(!row.hidden&&box&&!box.disabled){box.checked=true;}});updateSelectionCount();});document.getElementById("selection-clear").addEventListener("click",function(){boxes().forEach(function(box){if(!box.disabled){box.checked=false;}});updateSelectionCount();});boxes().forEach(function(box){box.addEventListener("change",updateSelectionCount);});refreshView();updateSelectionCount();}());</script>`)
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
