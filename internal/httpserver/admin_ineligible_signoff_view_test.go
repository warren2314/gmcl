package httpserver

import (
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func signOffFixture() ([]ineligibleSignOffCase, []ineligibleSignOffTask, time.Time, *time.Location) {
	loc := time.FixedZone("Europe/London", 3600)
	now := time.Date(2026, time.August, 24, 9, 0, 0, 0, loc)
	overdue := now.Add(-48 * time.Hour)
	cases := []ineligibleSignOffCase{{
		ID: 1176, Reference: "GMCL-2026-001176", Player: "Ronnie Harris", Club: "Mottram CC", Team: "1st XI",
		ApprovedAt: now.Add(-24 * time.Hour), Effects: []string{"player_ban", "points_adjustment"}, LeaguePoints: true,
	}}
	tasks := []ineligibleSignOffTask{{
		ID: 42, CaseID: 1176, Reference: "GMCL-2026-001176", Note: "Apply -10 league-table point adjustment", Status: "open", DueAt: &overdue,
	}}
	return cases, tasks, now, loc
}

func TestSignOffViewShowsOnlyTheSignOffWork(t *testing.T) {
	cases, tasks, now, loc := signOffFixture()
	output := httptest.NewRecorder()
	writeIneligibleSignOffView(output, "denver", cases, tasks, true, loc, now)
	html := output.Body.String()

	for _, want := range []string{
		"Ready for your final sign-off",
		"1 waiting",
		`href="/admin/cases/1176"`,
		"Sign off and issue",
		"Player ban, Points adjustment",
		"League points to apply in Play-Cricket",
		"Overdue",
		`href="/admin/ineligible?view=all"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("sign-off view is missing %q", want)
		}
	}

	// The whole point of this page is that the investigators' queue is not on it.
	for _, unwanted := range []string{
		"Team queue", "Reports to review", "Not yet selected", "Hidden reports",
		"Import and choose reports", "Report history", "Delivery exceptions",
		"Club replies to read", "Responses overdue",
	} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("sign-off view still shows the investigators' work: %q", unwanted)
		}
	}
}

func TestSignOffViewEscapesAndHandlesEmptyState(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, time.August, 24, 20, 0, 0, 0, loc)
	output := httptest.NewRecorder()
	writeIneligibleSignOffView(output, "Denver <Play-Cricket>", nil, nil, true, loc, now)
	html := output.Body.String()
	if strings.Contains(html, "Denver <Play-Cricket>") {
		t.Fatal("administrator name was not escaped")
	}
	for _, want := range []string{
		"Good evening, Denver &lt;Play-Cricket&gt;",
		"Nothing is waiting for your sign-off.",
		"No league-table adjustments are waiting",
		"0 waiting",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("empty sign-off view is missing %q", want)
		}
	}
}

func TestSignOffViewWarnsWhenTheAccountCannotIssue(t *testing.T) {
	cases, tasks, now, loc := signOffFixture()
	output := httptest.NewRecorder()
	writeIneligibleSignOffView(output, "denver", cases, tasks, false, loc, now)
	if !strings.Contains(output.Body.String(), "Sign-off access missing.") {
		t.Fatal("a sign-off queue the account cannot issue must say so")
	}

	output = httptest.NewRecorder()
	writeIneligibleSignOffView(output, "denver", nil, tasks, false, loc, now)
	if strings.Contains(output.Body.String(), "Sign-off access missing.") {
		t.Fatal("do not warn about issue access when nothing is waiting")
	}
}

func TestSignOffViewFlagsUnassignedPointsTasks(t *testing.T) {
	cases, tasks, now, loc := signOffFixture()
	tasks[0].Unassigned = true
	output := httptest.NewRecorder()
	writeIneligibleSignOffView(output, "denver", cases, tasks, true, loc, now)
	if !strings.Contains(output.Body.String(), "This task has no owner") {
		t.Fatal("an unassigned points task cannot be completed, so the page must explain it")
	}
}

func TestSignOffViewIsTheDefaultAndIsEscapable(t *testing.T) {
	if !ineligibleSignOffViewRequested(url.Values{}) {
		t.Fatal("the focused view must be the default for the sign-off administrator")
	}
	if !ineligibleSignOffViewRequested(url.Values{"scope": {"all"}, "state": {"open"}}) {
		t.Fatal("ordinary queue links must still land on the focused view")
	}
	if ineligibleSignOffViewRequested(url.Values{"view": {"all"}}) {
		t.Fatal("view=all must open the full queue")
	}
	if !ineligibleFullQueueRequested(url.Values{"view": {"all"}}) {
		t.Fatal("view=all must be recognised as the full queue")
	}
	if ineligibleFullQueueRequested(url.Values{}) {
		t.Fatal("the plain dashboard URL is not an explicit full-queue request")
	}
}

func TestSignOffIdentityMatchesTheSignOffButton(t *testing.T) {
	raw, err := os.ReadFile("admin_ineligible_signoff_view.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, `s.isActiveSanctionRecipientAdmin(ctx, adminID, "play_cricket")`) {
		t.Fatal("the sign-off queue must use the same identity test as the sign-off button")
	}
	for _, name := range []string{"denver", "Denver Thornton", "denverthornton"} {
		if strings.Contains(strings.ToLower(source), strings.ToLower(`"`+name+`"`)) {
			t.Fatalf("the sign-off view hard-codes the administrator %q instead of using the recipient directory", name)
		}
	}

	raw, err = os.ReadFile("admin_ineligible.go")
	if err != nil {
		t.Fatal(err)
	}
	dashboard := string(raw)
	signOff := strings.Index(dashboard, "s.writeAdminIneligibleSignOffPage(w, r, *currentAdminID, adminName)")
	queue := strings.Index(dashboard, "query, args := buildIneligibleQueueQueryForAdmin(filter, currentAdminID)")
	if signOff < 0 || queue < 0 || signOff > queue {
		t.Fatalf("the sign-off page must be chosen before the full queue is loaded: signOff=%d queue=%d", signOff, queue)
	}
	if !strings.Contains(dashboard, `href="/admin/ineligible">Back to outcomes to sign off`) {
		t.Fatal("the full queue does not offer a way back to the sign-off view")
	}
}
