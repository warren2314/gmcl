package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// adminCaseActionContext describes the current records and this viewer's
// capabilities. It is a read model: selecting an action never changes a case.
type adminCaseActionContext struct {
	Source, Status, OwnerName string
	HasProposed               bool
	OwnerReviewRequired       bool
	SentForApproval           bool
	ReplyUnreviewed           bool
	DeliveryException         bool
	HasResponseRequest        bool
	IsOwner                   bool
	CanInvestigate            bool
	CanPropose                bool
	CanApprove                bool
	CanIssue                  bool
	CanAppeal                 bool
	SourceReviewRequired      bool
	CanReviewSource           bool
	Unavailable               bool
	ResponseWindow            adminCaseResponseWindowView
}

type adminCaseNextAction struct {
	Title, Description, Responsible, Href, Label, Tone string
}

// These are existing route capabilities for secondary controls. Ineligible
// decision approval and final issue retain their separate source policies in
// adminCaseActionContext; an ordinary publish grant still permits notice resend.
type adminCaseWorkspacePermissions struct {
	CanInvestigate    bool
	CanPublishNotices bool
	CanTriage         bool
}

func safeAdminCaseReturnURL(value string) string {
	if value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" {
		return ""
	}
	switch parsed.Path {
	case "/admin/ineligible", "/admin/cases", "/admin/cases/mine/ineligible", "/admin/dashboard":
	default:
		return ""
	}
	query, err := url.QueryUnescape(parsed.RawQuery)
	if err != nil || strings.IndexFunc(query, unicode.IsControl) >= 0 || strings.IndexFunc(parsed.Fragment, unicode.IsControl) >= 0 {
		return ""
	}
	parsed.RawPath = ""
	return parsed.String()
}

// A short-lived, per-case navigation cookie survives existing POST redirects.
// It carries only an allowlisted local results URL, never authentication data.
func adminCaseReturnDestination(w http.ResponseWriter, r *http.Request, caseID int64, defaultLabel, defaultURL string) (string, string) {
	cookieName := "case_return_" + strconv.FormatInt(caseID, 10)
	cookiePath := "/admin/cases/" + strconv.FormatInt(caseID, 10)
	if r.Method == http.MethodGet && r.URL.Query().Has("return_to") {
		if destination := safeAdminCaseReturnURL(r.URL.Query().Get("return_to")); destination != "" {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: url.QueryEscape(destination), Path: cookiePath, MaxAge: 1800, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || strings.EqualFold(firstForwardedValue(r.Header.Get("X-Forwarded-Proto")), "https")})
			return "Back to results", destination
		}
		return defaultLabel, defaultURL
	}
	if cookie, err := r.Cookie(cookieName); err == nil {
		if value, err := url.QueryUnescape(cookie.Value); err == nil {
			if destination := safeAdminCaseReturnURL(value); destination != "" {
				return "Back to results", destination
			}
		}
	}
	return defaultLabel, defaultURL
}

func selectAdminCaseNextAction(view adminCaseActionContext, now time.Time) adminCaseNextAction {
	owner := strings.TrimSpace(view.OwnerName)
	if owner == "" {
		owner = "Case owner not assigned"
	}
	if view.Unavailable {
		return adminCaseNextAction{"Next action unavailable", "Some current workflow information could not be loaded. Refresh before deciding what to do next.", owner, "#case-history", "View case history", "warning"}
	}
	// Historical replies and response requests must not turn a completed case
	// back into a live investigation. Related delivery/task work stays separate.
	switch view.Status {
	case "published", "closed", "rejected", "withdrawn", "overturned", "expired":
		return adminCaseNextAction{"Case outcome recorded", "Review the recorded outcome and history. Delivery problems and follow-up tasks can still require separate action.", "Relevant task or communications owner", "#case-history", "View outcome and history", "secondary"}
	case "appealed":
		if !view.CanAppeal {
			return adminCaseNextAction{"Awaiting authorised appeal review", "The recorded appeal needs an administrator with appeal authority. You can inspect the decision and history.", "Authorised appeals administrator", "#case-history", "View appeal history", "secondary"}
		}
		return adminCaseNextAction{"Review the appeal", "Review the appeal and recorded decision before using the existing appeal or correction controls.", "Authorised appeals administrator", "#case-maintenance", "View appeal and correction controls", "warning"}
	case "approved":
		if view.Source == "ineligible_player" && view.SourceReviewRequired {
			description := "Linked source information has changed since this outcome was approved. Review it before final issue. The existing reopen action checks whether delivery or follow-up work already prevents reopening."
			if view.CanReviewSource {
				return adminCaseNextAction{"Review changed source evidence", description, "Authorised investigator or decision approver", "#source-change-review", "Review source change", "warning"}
			}
			return adminCaseNextAction{"Awaiting changed-source review", description, "Authorised investigator or decision approver", "#source-change-review", "View source-review requirement", "warning"}
		}
		responsible := "Authorised publisher"
		description := "The approved outcome is locked. The authorised publisher must issue the approved versions; delivery is tracked separately."
		if view.Source == "ineligible_player" {
			responsible = "Configured final issuer (Denver stage)"
			description = "Decision approval is complete. The configured final issuer must give separate final sign-off and issue the locked notices."
		}
		if view.CanIssue {
			return adminCaseNextAction{"Final sign-off and issue", description, responsible, "#decision-issue", "Review and issue locked outcome", "primary"}
		}
		return adminCaseNextAction{"Awaiting final issue", description, responsible, "#case-decision", "View approved outcome", "secondary"}
	case "decision_proposed":
		if view.OwnerReviewRequired && !view.SentForApproval {
			action := adminCaseNextAction{"Owner notice review", "The current proposal needs the assigned owner's review of the complete audience notices before decision approval.", owner, "#case-decision", "View current proposal", "warning"}
			if view.IsOwner && view.CanPropose {
				action.Href, action.Label = "#outcome-notice-review", "Review notices and submit for approval"
			}
			return action
		}
		description := "An authorised approver must review the submitted proposal and exact notices. Ordinary cases retain their different-person approval and authorised override rules."
		responsible := "Authorised decision approver"
		if view.Source == "ineligible_player" {
			description = "A designated ineligible-player approver may approve a proposal they prepared. Final sign-off and issue remain a separate responsibility."
			responsible = "Designated ineligible-player approver (Dave / Warren stage)"
		}
		if view.CanApprove {
			return adminCaseNextAction{"Decision approval", description, responsible, "#decision-approval", "Review decision and notices", "primary"}
		}
		return adminCaseNextAction{"Awaiting decision approval", description, responsible, "#case-decision", "View current proposal", "secondary"}
	case "triage":
		if view.HasProposed {
			return adminCaseNextAction{"Review calculated candidate", "This shadow-mode candidate is available for reconciliation and cannot be published. Review its proposed effects and source configuration.", "Authorised sanctions administrator", "#case-decision", "Review calculated candidate", "warning"}
		}
	}
	if view.ReplyUnreviewed {
		if !view.CanInvestigate {
			return adminCaseNextAction{"Awaiting reply review", "A received response needs an authorised investigator's review before decision preparation. You can read the recorded reply.", owner, "#club-response", "Read club reply", "secondary"}
		}
		return adminCaseNextAction{"Review the club reply", "A received response has not yet been marked reviewed. Read it before preparing a decision.", owner, "#club-response", "Read and review club reply", "warning"}
	}
	if view.DeliveryException {
		description := "Check the latest failed, bounced or complained notice and its retry state. Any resend still requires the existing notice-publishing authority."
		if view.CanInvestigate {
			return adminCaseNextAction{"Review delivery problem", description, owner + "; authorised communications staff", "#case-delivery", "Review notice delivery", "warning"}
		}
		return adminCaseNextAction{"Awaiting delivery follow-up", description, "Authorised investigator or communications owner", "#case-delivery", "View delivery history", "secondary"}
	}
	window := view.ResponseWindow
	if view.Status == "response_pending" && window.Status == "queued" {
		return adminCaseNextAction{"Awaiting response-request delivery", "The request is queued. The recorded response clock starts only after successful delivery; check delivery history if it remains queued.", "Communications owner", "#case-delivery", "View delivery history", "warning"}
	}
	if view.Status == "response_pending" && window.Status == "pending" {
		if window.DueAt == nil {
			return adminCaseNextAction{"Check the response deadline", "The current request has no recorded deadline. Check the response clock and delivery history before treating the response as due or overdue.", owner, "#response-window", "View response clock", "warning"}
		}
		if window.DueAt.After(now) {
			return adminCaseNextAction{"Awaiting club response", "The club response is pending until the recorded deadline below. A page refresh does not restart the clock.", owner + "; club response pending", "#response-window", "View response deadline", "secondary"}
		}
	}
	if window.Status == "expired" || (view.Status == "response_pending" && window.Status == "pending" && window.DueAt != nil && !window.DueAt.After(now)) {
		return adminCaseNextAction{"Review the overdue response", "The recorded response deadline has passed. Review the evidence and existing response policy; expiry does not decide the case automatically.", owner, "#response-window", "Review response clock", "warning"}
	}
	if view.Status == "response_pending" {
		return adminCaseNextAction{"Check current response work", "Review the current response request and any recorded correspondence before taking the next case action.", owner, "#case-actions", "View response work", "warning"}
	}
	if view.Status != "submitted" && view.Status != "triage" && view.Status != "investigating" {
		return adminCaseNextAction{"Review current case status", "Check the recorded case status and history before taking further action.", owner, "#case-history", "View case history", "secondary"}
	}
	if view.IsOwner && view.CanPropose {
		return adminCaseNextAction{"Review evidence and prepare decision", "Check the source report, evidence and published rule. Request a club response where needed, then prepare the findings and proposed effects.", owner, "#decision-preparation", "Open decision preparation", "primary"}
	}
	if view.CanInvestigate {
		return adminCaseNextAction{"Continue investigation", "Review the evidence and current correspondence. Decision preparation remains with the assigned case owner.", owner, "#case-actions", "Open investigation work", "primary"}
	}
	return adminCaseNextAction{"Awaiting case owner action", "The assigned investigator prepares the evidence, correspondence and decision. Your view does not grant investigation or proposal authority.", owner, "#case-background", "View case background", "secondary"}
}

// Keep existing decision forms in the current-work area rather than after the
// full history and evidence list. Services still authorise every POST.
func (s *Server) writeAdminCaseWorkspaceDecisionActions(w http.ResponseWriter, r *http.Request, caseID int64, csrf string, view adminCaseActionContext, canReject bool) {
	if view.Unavailable {
		fmt.Fprint(w, `<div class="alert alert-warning">Current decision actions are unavailable because workflow information could not be loaded. Refresh before continuing.</div>`)
		return
	}
	approvalReady := view.Status == "decision_proposed" && (!view.OwnerReviewRequired || view.SentForApproval)
	if approvalReady {
		fmt.Fprint(w, `<div id="decision-approval">`)
		if view.CanApprove {
			fmt.Fprint(w, adminCaseApprovalFormHTML(caseID, csrf, view.Source))
		} else {
			fmt.Fprint(w, `<div class="alert alert-info">This decision is awaiting its authorised approver. Your current access does not permit approval.</div>`)
		}
		fmt.Fprint(w, `</div>`)
	}
	if canReject && (approvalReady || (view.Status == "triage" && view.HasProposed)) {
		fmt.Fprintf(w, `<details class="card mb-4"><summary class="card-header">Return the proposal for changes</summary><form method="POST" action="/admin/cases/%d/reject"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><label class="form-label">Reason</label><textarea class="form-control" name="reason" required rows="2"></textarea><p class="form-text">The rejected revision remains in history and the case returns to investigation.</p><button class="btn btn-outline-secondary">Reject proposal</button></div></form></details>`, caseID, escapeHTML(csrf))
	}
	if view.Status != "approved" {
		return
	}
	fmt.Fprint(w, `<div id="decision-issue">`)
	if view.Source == "ineligible_player" && view.SourceReviewRequired {
		fmt.Fprint(w, adminCaseSourceReviewHTML(caseID, csrf, view.CanReviewSource))
		fmt.Fprint(w, `</div>`)
		return
	}
	if view.CanIssue {
		heading, label := "Issue approved outcome", "Issue locked outcome"
		if view.Source == "ineligible_player" {
			heading, label = "Denver final sign-off", "Final sign-off and issue outcomes"
		}
		fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/publish" class="card mb-4 border-primary"><input type="hidden" name="csrf_token" value="%s"><div class="card-header"><strong>%s</strong></div><div class="card-body"><p>This queues the exact locked notices for the required recipients. A no-action decision is delivered and closed without public-register publication.</p><button class="btn btn-primary">%s</button></div></form>`, caseID, escapeHTML(csrf), escapeHTML(heading), escapeHTML(label))
	} else if view.Source == "ineligible_player" {
		fmt.Fprint(w, `<div class="alert alert-info"><strong>Decision approval complete.</strong> Awaiting the configured final issuer's separate sign-off before the locked outcomes are issued.</div>`)
	} else {
		fmt.Fprint(w, `<div class="alert alert-info"><strong>Decision approval complete.</strong> Awaiting an authorised publisher to issue the locked outcomes.</div>`)
	}
	fmt.Fprint(w, `</div>`)
}

func adminCaseSourceReviewHTML(caseID int64, csrf string, canReview bool) string {
	var out strings.Builder
	fmt.Fprint(&out, `<section id="source-change-review" class="mb-4"><div class="alert alert-warning"><strong>Source review required before final issue.</strong> Linked intake information no longer matches the approved case. The approved notices remain locked; a page refresh does not update or reapprove them.</div>`)
	if canReview {
		fmt.Fprint(&out, adminIneligibleReopenFormHTML(caseID, csrf))
	} else {
		fmt.Fprint(&out, `<p class="text-muted">An authorised investigator or decision approver must review the source change. Your issue permission does not grant source-review authority.</p>`)
	}
	fmt.Fprint(&out, `</section>`)
	return out.String()
}

func adminCaseOwnerReviewSubmitHTML(caseID int64, csrf string) string {
	return fmt.Sprintf(`<form id="owner-outcome-submit" method="POST" action="/admin/cases/%d/send-for-approval" class="card mb-4 border-primary"><input type="hidden" name="csrf_token" value="%s"><div class="card-header"><strong>Final owner check</strong></div><div class="card-body"><p class="mb-0">Read the complete offending-club, reporting-club and official versions above. Only continue when the findings, rule, sanctions, appeal wording and audience differences are correct.</p></div><div class="card-footer"><button class="btn btn-primary">Submit reviewed notices for approval</button></div></form>`, caseID, escapeHTML(csrf))
}

func adminCaseNextActionHTML(action adminCaseNextAction, view adminCaseActionContext, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	// Tone is internal presentation data, but still bound the class and anchor.
	tone := action.Tone
	if tone != "primary" && tone != "warning" && tone != "secondary" {
		tone = "secondary"
	}
	href := action.Href
	if !strings.HasPrefix(href, "#") {
		href = "#case-actions"
	}
	deadline := ""
	if view.Status == "response_pending" && view.ResponseWindow.Status == "pending" && view.ResponseWindow.DueAt != nil {
		deadline = fmt.Sprintf(`<div class="small mt-2"><strong>Recorded deadline:</strong> <time datetime="%s">%s</time> (Europe/London)</div>`, view.ResponseWindow.DueAt.Format(time.RFC3339), escapeHTML(view.ResponseWindow.DueAt.In(loc).Format("02 Jan 2006 15:04")))
	}
	return fmt.Sprintf(`<section class="card border-%s mb-4 case-next-action" id="next-stage" aria-labelledby="case-next-action-title"><div class="card-body d-flex flex-column flex-lg-row justify-content-between gap-3"><div><p class="text-uppercase small fw-semibold text-muted mb-1">Next action</p><h2 class="h4 mb-2" id="case-next-action-title">%s</h2><p class="mb-2">%s</p><div class="small"><strong>Responsible:</strong> %s</div>%s</div><a class="btn btn-%s align-self-start" href="%s">%s</a></div></section>`, tone, escapeHTML(action.Title), escapeHTML(action.Description), escapeHTML(action.Responsible), deadline, tone, escapeHTML(href), escapeHTML(action.Label))
}
