package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCaseReturnURLRejectsExternalUnrelatedAndControlURLs(t *testing.T) {
	for _, value := range []string{"https://evil.test/admin/ineligible", "//evil.test/admin/cases", "http:evil.test", "/admin/users", "/admin/ineligible/elsewhere", "/admin/ineligible\n", "/admin/ineligible?q=%0d%0aLocation:evil", "/admin/ineligible?q=%zz", "/admin/ineligible#%0a", `/admin\ineligible`} {
		if got := safeAdminCaseReturnURL(value); got != "" {
			t.Errorf("accepted unsafe return destination %q as %q", value, got)
		}
	}
}

func TestCaseReturnContextPreservesFiltersThroughPostAndRedirect(t *testing.T) {
	destination := "/admin/ineligible?view=cases&scope=mine&q=Example+CC&page=3"
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/admin/cases/42?return_to="+url.QueryEscape(destination), nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	label, got := adminCaseReturnDestination(w, r, 42, "Back to cases", "/admin/cases")
	if label != "Back to results" || got != destination {
		t.Fatalf("return context = %q, %q", label, got)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Path != "/admin/cases/42" || cookie.MaxAge != 1800 || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("navigation cookie scope = %#v", cookie)
	}
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		next := httptest.NewRequest(method, "/admin/cases/42", nil)
		next.AddCookie(cookie)
		if _, got := adminCaseReturnDestination(httptest.NewRecorder(), next, 42, "Back to cases", "/admin/cases"); got != destination {
			t.Fatalf("%s return context = %q", method, got)
		}
	}
	other := httptest.NewRequest(http.MethodGet, "/admin/cases/43", nil)
	other.AddCookie(cookie)
	if _, got := adminCaseReturnDestination(httptest.NewRecorder(), other, 43, "Back to cases", "/admin/cases"); got != "/admin/cases" {
		t.Fatalf("another case reused return context: %q", got)
	}
}

func TestCaseReturnContextIgnoresTamperedCookie(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/admin/cases/42", nil)
	r.AddCookie(&http.Cookie{Name: "case_return_42", Value: url.QueryEscape("//evil.test/admin/cases")})
	if _, got := adminCaseReturnDestination(httptest.NewRecorder(), r, 42, "Back to cases", "/admin/cases"); got != "/admin/cases" {
		t.Fatalf("tampered return destination accepted: %q", got)
	}
}

func TestCaseReturnContextAllowsHomeWorkViews(t *testing.T) {
	for _, destination := range []string{"/admin/dashboard#my-cases", "/admin/dashboard?work=responses#my-work", "/admin/dashboard?work=decisions#my-work"} {
		if got := safeAdminCaseReturnURL(destination); got != destination {
			t.Errorf("Home return destination = %q, want %q", got, destination)
		}
	}
}

func TestCaseNextActionPreservesCurrentRevisionAndSourceAuthority(t *testing.T) {
	now := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		view        adminCaseActionContext
		title, href string
	}{
		{"owner must review current notices before approval", adminCaseActionContext{Status: "decision_proposed", Source: "ineligible_player", OwnerReviewRequired: true, IsOwner: true, CanPropose: true, CanApprove: true}, "Owner notice review", "#outcome-notice-review"},
		{"another viewer cannot submit owner review", adminCaseActionContext{Status: "decision_proposed", OwnerReviewRequired: true, CanApprove: true}, "Owner notice review", "#case-decision"},
		{"designated ineligible approver may have prepared proposal", adminCaseActionContext{Status: "decision_proposed", Source: "ineligible_player", OwnerReviewRequired: true, SentForApproval: true, IsOwner: true, CanApprove: true}, "Decision approval", "#decision-approval"},
		{"legacy revision without owner checkpoint", adminCaseActionContext{Status: "decision_proposed", CanApprove: true}, "Decision approval", "#decision-approval"},
		{"issuer cannot approve merely because they can issue", adminCaseActionContext{Status: "decision_proposed", Source: "ineligible_player", CanIssue: true}, "Awaiting decision approval", "#case-decision"},
		{"approved case goes to distinct issuer", adminCaseActionContext{Status: "approved", Source: "ineligible_player", CanIssue: true}, "Final sign-off and issue", "#decision-issue"},
		{"changed source precedes final issue", adminCaseActionContext{Status: "approved", Source: "ineligible_player", CanIssue: true, SourceReviewRequired: true, CanReviewSource: true}, "Review changed source evidence", "#source-change-review"},
		{"issuer cannot review changed source through issue access", adminCaseActionContext{Status: "approved", Source: "ineligible_player", CanIssue: true, SourceReviewRequired: true}, "Awaiting changed-source review", "#source-change-review"},
		{"closed case does not reopen on changed source", adminCaseActionContext{Status: "closed", Source: "ineligible_player", SourceReviewRequired: true, CanReviewSource: true}, "Case outcome recorded", "#case-history"},
		{"decision approver cannot issue without issue capability", adminCaseActionContext{Status: "approved", Source: "ineligible_player", CanApprove: true}, "Awaiting final issue", "#case-decision"},
		{"closed case ignores historical unreviewed reply", adminCaseActionContext{Status: "closed", ReplyUnreviewed: true, CanInvestigate: true}, "Case outcome recorded", "#case-history"},
		{"unreviewed reply precedes decision preparation", adminCaseActionContext{Status: "investigating", ReplyUnreviewed: true, IsOwner: true, CanPropose: true, CanInvestigate: true}, "Review the club reply", "#club-response"},
		{"proposal access does not permit reply review", adminCaseActionContext{Status: "investigating", ReplyUnreviewed: true, IsOwner: true, CanPropose: true}, "Awaiting reply review", "#club-response"},
		{"reply review precedes delivery problem", adminCaseActionContext{Status: "investigating", ReplyUnreviewed: true, DeliveryException: true, CanInvestigate: true}, "Review the club reply", "#club-response"},
		{"delivery problem precedes preparation", adminCaseActionContext{Status: "investigating", IsOwner: true, CanPropose: true, CanInvestigate: true, DeliveryException: true}, "Review delivery problem", "#case-delivery"},
		{"proposal access does not permit delivery follow-up", adminCaseActionContext{Status: "investigating", IsOwner: true, CanPropose: true, DeliveryException: true}, "Awaiting delivery follow-up", "#case-delivery"},
		{"completed delivery issue stays separate", adminCaseActionContext{Status: "closed", DeliveryException: true, CanInvestigate: true}, "Case outcome recorded", "#case-history"},
		{"appeal action requires appeal permission", adminCaseActionContext{Status: "appealed", CanInvestigate: true}, "Awaiting authorised appeal review", "#case-history"},
		{"authorised appeal reviewer", adminCaseActionContext{Status: "appealed", CanAppeal: true}, "Review the appeal", "#case-maintenance"},
		{"owner without proposal permission", adminCaseActionContext{Status: "investigating", IsOwner: true, CanInvestigate: true}, "Continue investigation", "#case-actions"},
		{"unknown status never invents proposal action", adminCaseActionContext{Status: "unknown", IsOwner: true, CanPropose: true}, "Review current case status", "#case-history"},
		{"failed supporting read", adminCaseActionContext{Status: "investigating", IsOwner: true, CanPropose: true, Unavailable: true}, "Next action unavailable", "#case-history"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := selectAdminCaseNextAction(test.view, now)
			if got.Title != test.title || got.Href != test.href {
				t.Fatalf("action = %q (%s), want %q (%s)", got.Title, got.Href, test.title, test.href)
			}
		})
	}
}

func TestCaseNextActionUsesRecordedResponseClock(t *testing.T) {
	now := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	before, after := now.Add(-time.Second), now.Add(time.Second)
	tests := []struct {
		name, state string
		due         *time.Time
		want        string
	}{
		{"queued request has no running clock", "queued", &before, "Awaiting response-request delivery"},
		{"before recorded deadline", "pending", &after, "Awaiting club response"},
		{"exact deadline is overdue", "pending", &now, "Review the overdue response"},
		{"after recorded deadline", "pending", &before, "Review the overdue response"},
		{"missing deadline is not overdue", "pending", nil, "Check the response deadline"},
		{"cancelled request is not a current clock", "cancelled", &before, "Check current response work"},
		{"expired request remains reviewable", "expired", &before, "Review the overdue response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := adminCaseActionContext{Status: "response_pending", ResponseWindow: adminCaseResponseWindowView{Status: test.state, DueAt: test.due}}
			if got := selectAdminCaseNextAction(view, now); got.Title != test.want {
				t.Fatalf("action = %q, want %q", got.Title, test.want)
			}
		})
	}
}

func TestCaseNextActionExplainsIneligibleExceptionWithoutApplyingItToOtherSources(t *testing.T) {
	view := adminCaseActionContext{Status: "decision_proposed", CanApprove: true}
	ordinary := selectAdminCaseNextAction(view, time.Now())
	view.Source = "ineligible_player"
	ineligible := selectAdminCaseNextAction(view, time.Now())
	if !strings.Contains(ineligible.Description, "may approve a proposal they prepared") || !strings.Contains(ineligible.Description, "separate responsibility") {
		t.Fatalf("ineligible routing explanation = %s", ineligible.Description)
	}
	if strings.Contains(ordinary.Description, "may approve a proposal they prepared") || !strings.Contains(ordinary.Description, "different-person") {
		t.Fatalf("ordinary routing explanation = %s", ordinary.Description)
	}
}

func TestCaseNextActionEscapesContextAndShowsOnlyCurrentDeadline(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	view := adminCaseActionContext{Status: "response_pending", OwnerName: `<script>owner</script>`, ResponseWindow: adminCaseResponseWindowView{Status: "pending", DueAt: &due}}
	html := adminCaseNextActionHTML(selectAdminCaseNextAction(view, due.Add(-time.Hour)), view, loc)
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;owner&lt;/script&gt;") || !strings.Contains(html, "15 Oct 2026 15:00") {
		t.Fatalf("unsafe or incorrect next-action context: %s", html)
	}
	view.Status = "closed"
	html = adminCaseNextActionHTML(selectAdminCaseNextAction(view, due), view, loc)
	if strings.Contains(html, "Recorded deadline") {
		t.Fatalf("completed case displays old deadline as current: %s", html)
	}
}

func TestCaseResponseReadOnlyViewerSeesUnreviewedStateWithoutMutationForm(t *testing.T) {
	html := adminCaseResponseHTML(42, "csrf", adminCaseResponseView{ID: 8, Unreviewed: true, ReceivedAt: time.Now(), Body: "Club explanation"}, time.UTC, false)
	if !strings.Contains(html, "Needs review") || !strings.Contains(html, "Club explanation") || strings.Contains(html, `action="/admin/cases/42/response-reviewed"`) {
		t.Fatalf("read-only response rendering lost state or exposed review form: %s", html)
	}
}

func TestCaseWorkspaceApprovalControlsFollowAuthorityAndOwnerCheckpoint(t *testing.T) {
	server := &Server{}
	for _, test := range []struct {
		name     string
		view     adminCaseActionContext
		wantForm bool
	}{
		{"designated approval", adminCaseActionContext{Status: "decision_proposed", Source: "ineligible_player", CanApprove: true}, true},
		{"owner review incomplete", adminCaseActionContext{Status: "decision_proposed", OwnerReviewRequired: true, CanApprove: true}, false},
		{"issuer waiting for approval", adminCaseActionContext{Status: "decision_proposed", Source: "ineligible_player", CanIssue: true}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			server.writeAdminCaseWorkspaceDecisionActions(w, httptest.NewRequest("GET", "/admin/cases/42", nil), 42, `csrf"value`, test.view, false)
			html := w.Body.String()
			hasForm := strings.Contains(html, `action="/admin/cases/42/approve"`)
			if hasForm != test.wantForm {
				t.Fatalf("approval form=%v, want %v: %s", hasForm, test.wantForm, html)
			}
			if hasForm && !strings.Contains(html, `value="csrf&quot;value"`) {
				t.Fatalf("CSRF value was not retained safely: %s", html)
			}
		})
	}
}

func TestCaseWorkspaceStaleApprovedSourceDoesNotOfferIssue(t *testing.T) {
	server := &Server{}
	for _, canReview := range []bool{false, true} {
		w := httptest.NewRecorder()
		view := adminCaseActionContext{Status: "approved", Source: "ineligible_player", SourceReviewRequired: true, CanReviewSource: canReview, CanIssue: true}
		server.writeAdminCaseWorkspaceDecisionActions(w, httptest.NewRequest("GET", "/admin/cases/42", nil), 42, `csrf"value`, view, false)
		html := w.Body.String()
		if strings.Contains(html, `action="/admin/cases/42/publish"`) || !strings.Contains(html, "Source review required before final issue") || !strings.Contains(html, "notices remain locked") {
			t.Fatalf("stale approval offered issue or omitted review context: %s", html)
		}
		if strings.Contains(html, `action="/admin/cases/42/reopen-source-change"`) != canReview {
			t.Fatalf("source review form did not follow capability=%v: %s", canReview, html)
		}
		if canReview && !strings.Contains(html, `value="csrf&quot;value"`) {
			t.Fatalf("source review CSRF value was not escaped: %s", html)
		}
	}
}

func TestCaseWorkspaceUnknownSourceReadDoesNotOfferIssue(t *testing.T) {
	server := &Server{}
	w := httptest.NewRecorder()
	server.writeAdminCaseWorkspaceDecisionActions(w, httptest.NewRequest("GET", "/admin/cases/42", nil), 42, "csrf", adminCaseActionContext{Status: "approved", Source: "ineligible_player", CanIssue: true, Unavailable: true}, false)
	if html := w.Body.String(); strings.Contains(html, "/publish") || !strings.Contains(html, "unavailable") {
		t.Fatalf("unknown source-read state offered issue: %s", html)
	}
}

func TestCaseWorkspaceReadOnlyClockKeepsDeadlineWithoutCorrection(t *testing.T) {
	started := time.Date(2026, time.October, 10, 9, 0, 0, 0, time.UTC)
	reminder, due := responseDeliveryDeadlines(started)
	clock := adminCaseResponseWindowHTMLAt(42, "csrf", adminCaseResponseWindowView{ID: 1, Status: "pending", DeliveredAt: &started, ReminderDueAt: &reminder, DueAt: &due}, "response_pending", time.UTC, started.Add(time.Hour), adminCaseWorkspacePermissions{})
	for _, want := range []string{"Club response clock", "Response deadline", "17 Oct 2026 09:00"} {
		if !strings.Contains(clock, want) {
			t.Errorf("read-only clock lost %q", want)
		}
	}
	if strings.Contains(clock, "/response-window/correct") {
		t.Fatal("viewer without investigation access was offered clock correction")
	}
}

func TestCaseWorkspaceReadOnlyEvidenceKeepsDisclosureRecordWithoutForms(t *testing.T) {
	sourceID := int64(71)
	for _, state := range []adminEvidenceDisclosureState{
		{Available: true},
		{SourceEvidenceID: &sourceID, Reviewer: "Synthetic reviewer", SharingAction: "shared", Available: true, Eligible: true},
	} {
		output := adminEvidenceDisclosureControlsHTML(42, 72, "csrf", state, adminCaseWorkspacePermissions{})
		if strings.Contains(output, "<form") || strings.Contains(output, "share-with-offending-club") {
			t.Fatal("read-only evidence record offered an investigation mutation")
		}
		if state.SourceEvidenceID != nil && (!strings.Contains(output, "Synthetic reviewer") || !strings.Contains(output, "Shared with offending club")) {
			t.Fatal("read-only evidence record lost its reviewer or disclosure state")
		}
		if state.SourceEvidenceID == nil && !strings.Contains(output, "Private source evidence") {
			t.Fatal("read-only evidence record lost the private-source label")
		}
	}
}
