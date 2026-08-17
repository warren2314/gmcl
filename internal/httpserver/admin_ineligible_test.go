package httpserver

import (
	"bytes"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseIneligibleQueueFiltersDefaultsAndRejectsUnknownValues(t *testing.T) {
	got := parseIneligibleQueueFilters(url.Values{
		"state":       {"not-a-state"},
		"origin":      {"untrusted"},
		"case_status": {"made-up"},
		"age":         {"500 years"},
		"scope":       {"not-a-scope"},
		"player":      {"  A Player  "},
	})
	if got.State != "open" || got.Origin != "" || got.CaseStatus != "" || got.Age != "" || got.Scope != "all" || got.Worklist != "visible" || got.Sort != "newest" {
		t.Fatalf("unexpected normalised filters: %#v", got)
	}
	if got.Player != "A Player" {
		t.Fatalf("player filter was not trimmed: %q", got.Player)
	}
}

func TestParseIneligibleQueueFiltersNormalisesFixtureRangeAndSort(t *testing.T) {
	got := parseIneligibleQueueFilters(url.Values{"fixture_from": {"2026-08-10"}, "fixture_to": {"2026-07-01"}, "sort": {"fixture_oldest"}})
	if got.FixtureFrom != "2026-07-01" || got.FixtureTo != "2026-08-10" || got.Sort != "fixture_oldest" {
		t.Fatalf("normalised fixture filters = %#v", got)
	}
	invalid := parseIneligibleQueueFilters(url.Values{"fixture_from": {"10/08/2026"}, "fixture_to": {"not-a-date"}, "sort": {"random"}})
	if invalid.FixtureFrom != "" || invalid.FixtureTo != "" || invalid.Sort != "newest" {
		t.Fatalf("invalid fixture filters were accepted: %#v", invalid)
	}
}

func TestIneligibleUnreadReplyFilterTargetsOnlyCasesAwaitingReview(t *testing.T) {
	filter := parseIneligibleQueueFilters(url.Values{
		"state":        {"all"},
		"worklist":     {"all"},
		"reply_status": {"unreviewed"},
	})
	if filter.ReplyStatus != "unreviewed" {
		t.Fatalf("reply status = %q, want unreviewed", filter.ReplyStatus)
	}
	query, args := buildIneligibleQueueQuery(filter)
	for _, want := range []string{
		"LEFT JOIN LATERAL",
		"latest_reply.needs_review",
		"latest_reply.created_at DESC NULLS LAST",
		"reviewed.metadata->>'response_event_id'=response.id::text",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("unreviewed-reply query missing %q: %s", want, query)
		}
	}
	if len(args) != 0 {
		t.Fatalf("unreviewed-reply query args = %#v, want none", args)
	}

	invalid := parseIneligibleQueueFilters(url.Values{"reply_status": {"anything"}})
	if invalid.ReplyStatus != "" {
		t.Fatalf("invalid reply filter was accepted: %#v", invalid)
	}
}

func TestIneligibleNewRepliesHrefOpensOneCaseOrFilteredReplyList(t *testing.T) {
	if got := ineligibleNewRepliesHref(ineligibleDashboardCounts{RecentReplies: 1, RecentReplyCaseID: 77}); got != "/admin/cases/77#club-response" {
		t.Fatalf("single reply href = %q", got)
	}
	got := ineligibleNewRepliesHref(ineligibleDashboardCounts{RecentReplies: 3})
	if !strings.Contains(got, "reply_status=unreviewed") || !strings.HasSuffix(got, "#reports") {
		t.Fatalf("multiple replies href = %q", got)
	}
}

func TestIneligibleCaseNextStepShowsReplyCountStatusAndDirectAnchor(t *testing.T) {
	caseID := int64(77)
	received := time.Date(2026, time.August, 14, 8, 15, 0, 0, time.UTC)
	html := ineligibleCaseNextStepHTML(ineligibleQueueRow{
		CaseID:           &caseID,
		CaseReference:    "GMCL-2026-0191",
		CaseStatus:       "investigating",
		Assignee:         "Dave",
		ReplyCount:       2,
		LatestReplyAt:    &received,
		ReplyNeedsReview: true,
	}, time.UTC)
	for _, want := range []string{
		`href="/admin/cases/77#club-response"`,
		"Reply received - needs review",
		"2 replies total",
		"Latest 14 Aug 08:15",
		"Dave",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("reply-aware case action missing %q: %s", want, html)
		}
	}
}
func TestWriteIneligibleStartRoutesShowsThreePlainLanguageChoices(t *testing.T) {
	var out bytes.Buffer
	writeIneligibleStartRoutes(&out, "csrf-token", 42)
	html := out.String()
	for _, want := range []string{"Raise one case", "Import and choose reports", "Import historical tracker", "Open next selected report", "/admin/ineligible/42"} {
		if !strings.Contains(html, want) {
			t.Errorf("start routes missing %q", want)
		}
	}
	if strings.Contains(html, "Check for new reports") || strings.Contains(html, ">Excel import<") {
		t.Fatalf("start routes retained old busy labels: %s", html)
	}
}

func TestIneligibleAdvancedControlsStayClosedForDefaultView(t *testing.T) {
	filter := parseIneligibleQueueFilters(url.Values{})
	if ineligibleQueueUsesAdvancedView(filter) {
		t.Fatalf("default filter unexpectedly opens manager controls: %#v", filter)
	}
	filter.Origin = "google_form"
	if ineligibleQueueUsesAdvancedView(filter) {
		t.Fatal("the Google import result should stay focused on its report queue")
	}
	filter.Player = "Alex Player"
	if !ineligibleQueueUsesAdvancedView(filter) {
		t.Fatal("an active player filter must open manager controls")
	}
}

func TestIneligibleCreateCaseFormUsesFourChecksAndOnePrimaryAction(t *testing.T) {
	recorder := httptest.NewRecorder()
	fixtureDate := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	writeIneligibleCreateCaseForm(recorder, "csrf-token", 7, "Reporting CC", "Alex Player", &fixtureDate, []byte(`{"Reason you believe the player is ineligible":"Not registered"}`), []ineligibleClubOption{{ID: 1, Name: "Reporting CC"}}, []ineligibleTeamOption{{ID: 2, ClubName: "Offending CC", TeamName: "1st XI"}}, false)
	html := recorder.Body.String()
	for _, want := range []string{"Raise this case", "1. Offending team", "2. Reporting club", "3. Fixture date", "4. Player", "Review case wording", ">Raise case<", `name="public_summary"`, `name="private_summary"`} {
		if !strings.Contains(html, want) {
			t.Errorf("create-case form missing %q", want)
		}
	}
	for _, forbidden := range []string{"Create case without sending email", "btn btn-danger"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("create-case form retained %q", forbidden)
		}
	}
}

func TestBackfillQuickReviewIsLimitedToClearRows(t *testing.T) {
	intakeID := int64(42)
	row := ineligibleBackfillRowView{MatchStatus: "matched_exact", MatchedIntakeID: &intakeID}
	if !ineligibleBackfillRowCanUseQuickReview(row, "open") {
		t.Fatal("clear exact match should allow quick review")
	}
	row.RequiresEffectReview = true
	if ineligibleBackfillRowCanUseQuickReview(row, "open") {
		t.Fatal("points or cards interpretation must require the full review form")
	}
	row.RequiresEffectReview = false
	row.Exception = "ambiguous source identity"
	if ineligibleBackfillRowCanUseQuickReview(row, "open") {
		t.Fatal("an exception must require the full review form")
	}
	row.Exception = ""
	if ineligibleBackfillRowCanUseQuickReview(row, "needs_interpretation") {
		t.Fatal("unknown historical state must require the full review form")
	}
}

func TestBuildIneligibleQueueQuerySupportsMyWorkAndOldestFirst(t *testing.T) {
	adminID := int32(42)
	query, args := buildIneligibleQueueQueryForAdmin(ineligibleQueueFilters{
		State: "all",
		Scope: "mine",
		Sort:  "oldest",
	}, &adminID)
	if !strings.Contains(query, "c.assigned_admin_id=$1") {
		t.Fatalf("my-work ownership filter missing: %s", query)
	}
	if !strings.Contains(query, "COALESCE(i.external_created_at,i.created_at) ASC,i.id ASC") {
		t.Fatalf("oldest-first ordering missing: %s", query)
	}
	if !reflect.DeepEqual(args, []any{adminID}) {
		t.Fatalf("query args = %#v, want admin id", args)
	}
}

func TestBuildIneligibleQueueQuerySupportsFixtureRangeAndOrder(t *testing.T) {
	query, args := buildIneligibleQueueQuery(ineligibleQueueFilters{State: "all", Scope: "all", FixtureFrom: "2026-07-01", FixtureTo: "2026-08-10", Sort: "fixture_oldest"})
	for _, want := range []string{"i.fixture_date >= $1::date", "i.fixture_date <= $2::date"} {
		if !strings.Contains(query, want) {
			t.Errorf("fixture query missing %q: %s", want, query)
		}
	}
	if !strings.Contains(query, "ORDER BY i.fixture_date ASC NULLS LAST,COALESCE(i.external_created_at,i.created_at) ASC,i.id ASC") {
		t.Fatalf("fixture ordering missing or unstable: %s", query)
	}
	wantArgs := []any{"2026-07-01", "2026-08-10"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("query args = %#v, want %#v", args, wantArgs)
	}
}

func TestWriteIneligibleFixtureDateControlsIsCompactAndPreservesFilters(t *testing.T) {
	var out bytes.Buffer
	writeIneligibleFixtureDateControls(&out, ineligibleQueueFilters{State: "open", Scope: "all", Worklist: "visible", Player: `Alex"><script>alert(1)</script>`, FixtureFrom: "2026-07-01", FixtureTo: "2026-08-10", Sort: "fixture_oldest"})
	html := out.String()
	for _, want := range []string{
		"Fixture dates",
		`name="fixture_from" value="2026-07-01"`,
		`name="fixture_to" value="2026-08-10"`,
		`<option value="fixture_oldest" selected>Oldest fixture first</option>`,
		`name="player" value="Alex&quot;&gt;&lt;script&gt;alert(1)&lt;/script&gt;"`,
		"Apply dates",
		"Clear dates",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("fixture controls missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `<script>alert(1)</script>`) {
		t.Fatalf("fixture controls contain unescaped filter input: %s", html)
	}
}

func TestWriteIneligibleEmptyQueueExplainsDateRangeAndClearsOnlyDates(t *testing.T) {
	var out bytes.Buffer
	filter := ineligibleQueueFilters{State: "open", Scope: "all", Worklist: "visible", Player: "Alex Player", FixtureFrom: "2026-07-01", FixtureTo: "2026-08-10", Sort: "fixture_oldest"}
	writeIneligibleEmptyQueue(&out, filter, "Google Form reports ready for review")
	html := out.String()
	if !strings.Contains(html, "No reports match these fixture dates") || !strings.Contains(html, "Clear dates") {
		t.Fatalf("date-filtered empty state is unclear: %s", html)
	}
	if strings.Contains(html, "No reports are currently selected") {
		t.Fatalf("date-filtered empty state incorrectly claims there is no selection: %s", html)
	}
	href := ineligibleClearFixtureDatesURL(filter)
	parsed, err := url.Parse(href)
	if err != nil {
		t.Fatalf("parse clear-dates URL: %v", err)
	}
	query := parsed.Query()
	if query.Get("fixture_from") != "" || query.Get("fixture_to") != "" || query.Get("player") != "Alex Player" || query.Get("sort") != "fixture_oldest" {
		t.Fatalf("clear-dates URL lost other filters or retained dates: %s", href)
	}
}

func TestIneligibleQueueTabURLPreservesFixtureView(t *testing.T) {
	href := ineligibleQueueTabURL(ineligibleQueueFilters{FixtureFrom: "2026-07-01", FixtureTo: "2026-08-10", Sort: "fixture_newest"}, "mine", "all", "visible")
	parsed, err := url.Parse(href)
	if err != nil {
		t.Fatalf("parse tab URL: %v", err)
	}
	query := parsed.Query()
	if query.Get("scope") != "mine" || query.Get("fixture_from") != "2026-07-01" || query.Get("fixture_to") != "2026-08-10" || query.Get("sort") != "fixture_newest" {
		t.Fatalf("tab URL lost fixture view: %s", href)
	}
}
func TestIneligibleFullHistoryURLUsesEveryState(t *testing.T) {
	href := ineligibleQueueTabURL(ineligibleQueueFilters{Sort: "fixture_newest"}, "all", "all", "all")
	parsed, err := url.Parse(href)
	if err != nil {
		t.Fatalf("parse full-history URL: %v", err)
	}
	query := parsed.Query()
	if query.Get("scope") != "all" || query.Get("state") != "all" || query.Get("worklist") != "all" {
		t.Fatalf("full-history URL is incomplete: %s", href)
	}
}

func TestLegacyDashboardCaseStatusLinkUsesAllWork(t *testing.T) {
	filter := parseIneligibleQueueFilters(url.Values{
		"state":       {"all"},
		"case_status": {"investigating"},
	})
	if filter.Scope != "all" {
		t.Fatalf("legacy dashboard link scope = %q, want all", filter.Scope)
	}

	filter = parseIneligibleQueueFilters(url.Values{
		"state":       {"all"},
		"case_status": {"investigating"},
		"scope":       {"mine"},
	})
	if filter.Scope != "mine" {
		t.Fatalf("explicit scope = %q, want mine", filter.Scope)
	}
}

func TestPlainIneligibleStatusExplainsWorkflowTerms(t *testing.T) {
	tests := map[string]string{
		"linked":            "Case raised",
		"response_pending":  "Waiting for response",
		"decision_proposed": "Decision ready for approval",
	}
	for value, want := range tests {
		if got := plainIneligibleStatus(value); got != want {
			t.Errorf("plainIneligibleStatus(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestBuildIneligibleQueueQueryUsesArgumentsForUserInput(t *testing.T) {
	filter := ineligibleQueueFilters{
		State:         "linked",
		Origin:        "google_form",
		ReportingClub: "Reporting CC",
		OffendingClub: "Offending CC",
		Team:          "1st XI",
		Player:        "A Player",
		Assignee:      "hussan",
		CaseStatus:    "investigating",
		Age:           "7d",
	}
	query, args := buildIneligibleQueueQuery(filter)
	for _, value := range []string{"Reporting CC", "Offending CC", "1st XI", "A Player", "hussan"} {
		if strings.Contains(query, value) {
			t.Fatalf("query interpolates user input %q: %s", value, query)
		}
	}
	want := []any{"linked", "google_form", "%Reporting CC%", "%Offending CC%", "%1st XI%", "%A Player%", "%hussan%", "investigating"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("query args = %#v, want %#v", args, want)
	}
	if !strings.Contains(query, "interval '7 days'") {
		t.Fatalf("validated age filter missing from query: %s", query)
	}
	if !strings.Contains(query, "sanction_intake_attachments") {
		t.Fatalf("queue query does not include evidence attachment counts: %s", query)
	}
}

func TestIneligibleAttachmentDispositionAllowsSafeMediaInline(t *testing.T) {
	for _, mediaType := range []string{"image/jpeg", "image/png", "image/webp", "image/gif; charset=binary", "video/mp4"} {
		if got := ineligibleAttachmentDisposition(mediaType, true); got != "inline" {
			t.Errorf("%s disposition = %q, want inline", mediaType, got)
		}
	}
	for _, mediaType := range []string{"image/heic", "image/svg+xml", "application/pdf", "text/html", "video/webm"} {
		if got := ineligibleAttachmentDisposition(mediaType, true); got != "attachment" {
			t.Errorf("%s disposition = %q, want attachment", mediaType, got)
		}
	}
	if got := ineligibleAttachmentPreviewKind("video/mp4; codecs=avc1"); got != "video" {
		t.Fatalf("MP4 preview kind = %q, want video", got)
	}
	if got := ineligibleAttachmentDisposition("image/jpeg", false); got != "attachment" {
		t.Fatalf("non-preview image disposition = %q, want attachment", got)
	}
}

func TestSourceStringFieldReadsExactGoogleFormHeaders(t *testing.T) {
	raw := []byte(`{
		"Email address":"reporter@example.com",
		"Name of defaulting player as shown on scorecard":"Alex Player",
		"Reason you believe the player is ineligible":"Not registered before the deadline",
		"Your Club":"Reporting CC",
		"Your Name & Role at Club/League":"Robin Reporter - Secretary",
		"Your Preferred tel no":"07123 456789"
	}`)
	tests := map[string]string{
		"email":  sourceStringField(raw, "Email address"),
		"player": sourceStringField(raw, "Name of defaulting player as shown on scorecard"),
		"reason": sourceStringField(raw, "Reason you believe the player is ineligible"),
		"club":   sourceStringField(raw, "Your Club"),
		"person": sourceStringField(raw, "Your Name & Role at Club/League"),
		"phone":  sourceStringField(raw, "Your Preferred tel no"),
	}
	want := map[string]string{
		"email":  "reporter@example.com",
		"player": "Alex Player",
		"reason": "Not registered before the deadline",
		"club":   "Reporting CC",
		"person": "Robin Reporter - Secretary",
		"phone":  "07123 456789",
	}
	if !reflect.DeepEqual(tests, want) {
		t.Fatalf("source fields = %#v, want %#v", tests, want)
	}
}

func TestIneligibleDefaultPublicSummaryIncludesFixture(t *testing.T) {
	date := time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC)
	got := ineligibleDefaultPublicSummary("Alex Player", &date)
	for _, want := range []string{"Alex Player", "04 August 2026"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q does not contain %q", got, want)
		}
	}
}

func TestReadIneligibleRetainedUploadConfinesStorageKey(t *testing.T) {
	root := t.TempDir()
	retained := filepath.Join(root, "sha256", "ab", "evidence.pdf")
	if err := os.MkdirAll(filepath.Dir(retained), 0700); err != nil {
		t.Fatal(err)
	}
	want := []byte("private retained evidence")
	if err := os.WriteFile(retained, want, 0600); err != nil {
		t.Fatal(err)
	}

	got, err := readIneligibleRetainedUpload(root, "sha256/ab/evidence.pdf")
	if err != nil {
		t.Fatalf("read valid retained upload: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("retained bytes = %q, want %q", got, want)
	}

	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte("must remain unreachable"), 0600); err != nil {
		t.Fatal(err)
	}
	relativeEscape, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	for _, storageKey := range []string{filepath.ToSlash(relativeEscape), filepath.ToSlash(outside)} {
		if _, err := readIneligibleRetainedUpload(root, storageKey); err == nil {
			t.Fatalf("escaping storage key %q was accepted", storageKey)
		}
	}
}

func TestReadIneligibleRetainedUploadRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte("must remain unreachable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.pdf")); err != nil {
		t.Skipf("symlinks are unavailable on this platform: %v", err)
	}
	if _, err := readIneligibleRetainedUpload(root, "escape.pdf"); err == nil {
		t.Fatal("symlink escaping the retained-upload root was accepted")
	}
}

func TestNextIneligibleReportIDSkipsLinkedAndResolvedRows(t *testing.T) {
	caseID := int64(100)
	queue := []ineligibleQueueRow{
		{ID: 1, State: "linked", CaseID: &caseID},
		{ID: 2, State: "duplicate"},
		{ID: 3, State: "reviewing"},
	}
	if got := nextIneligibleReportID(queue); got != 3 {
		t.Fatalf("next report = %d, want 3", got)
	}
	if got := nextIneligibleReportID(queue[:2]); got != 0 {
		t.Fatalf("resolved queue returned report %d", got)
	}
}

func TestDuplicateCaseLinkUsesNeutralAction(t *testing.T) {
	className, label := ineligibleCaseLinkAction("duplicate")
	if strings.Contains(className, "success") || label != "Open related case" {
		t.Fatalf("duplicate action = %q / %q", className, label)
	}
	className, label = ineligibleCaseLinkAction("primary")
	if !strings.Contains(className, "success") || label != "Open case and continue" {
		t.Fatalf("active action = %q / %q", className, label)
	}
}

func TestBackfillRowCountsAreUniqueAndPendingOnly(t *testing.T) {
	intakeID := int64(42)
	reviewID := int64(7)
	rows := []ineligibleBackfillRowView{
		{MatchStatus: "matched_exact", MatchedIntakeID: &intakeID, TrackerStateHint: "open"},
		{MatchStatus: "unmatched", TrackerStateHint: "unknown", RequiresEffectReview: true},
		{MatchStatus: "ambiguous", TrackerStateHint: "unknown", RequiresEffectReview: true, ReviewID: &reviewID},
	}
	pending, verified, suggested, needsHelp := ineligibleBackfillRowCounts(rows)
	if pending != 2 || verified != 1 || suggested != 1 || needsHelp != 1 {
		t.Fatalf("counts = pending %d, verified %d, suggested %d, needs help %d", pending, verified, suggested, needsHelp)
	}
}

func TestV8DecisionHistoryOnlyShowsCurrentDecisionFields(t *testing.T) {
	history := map[string]string{
		"Initial Exec Comments (Please put Dates & Names)":         "legacy comment",
		"Investigation Required (Yes/No)?":                         "Yes",
		"Responsible Officer?":                                     "Old officer",
		"Email Sent Date":                                          "01/01/2024",
		"Offending Club Response Received? (Yes/No)":               "Yes",
		"Offending Club Response Date?":                            "02/01/2024",
		"Offending Club Response Text":                             "  Club response  ",
		"Ready for Final Decision ":                                "Yes",
		"POINTS deduction":                                         "  12  ",
		"Cards":                                                    "Red",
		"Outcome Comms Shared with reporting and offending clubs?": "Yes",
		"Case Closed? (Yes/No)":                                    "Yes",
	}
	want := []v8DecisionHistoryField{
		{Label: "Offending Club Response Text", Value: "Club response"},
		{Label: "POINTS deduction", Value: "12"},
		{Label: "Cards", Value: "Red"},
	}
	if got := v8DecisionHistoryFields(history); !reflect.DeepEqual(got, want) {
		t.Fatalf("decision history fields = %#v, want %#v", got, want)
	}
	var out bytes.Buffer
	writeIneligibleManualHistory(&out, ineligibleBackfillRowView{ManualHistory: history})
	html := out.String()
	for _, wanted := range []string{"Decision-relevant V8 history", "Offending Club Response Text", "POINTS deduction", "Cards"} {
		if !strings.Contains(html, wanted) {
			t.Fatalf("rendered V8 history missing %q: %s", wanted, html)
		}
	}
	for _, obsolete := range []string{"Initial Exec Comments", "Responsible Officer", "Email Sent Date", "Case Closed"} {
		if strings.Contains(html, obsolete) {
			t.Fatalf("rendered V8 history still contains obsolete field %q: %s", obsolete, html)
		}
	}
	out.Reset()
	writeIneligibleManualHistory(&out, ineligibleBackfillRowView{ManualHistory: map[string]string{"Case Closed? (Yes/No)": "Yes"}})
	if out.Len() != 0 {
		t.Fatalf("obsolete-only history rendered an empty panel: %s", out.String())
	}
}

func TestIneligibleCaseDashboardGroupsShareExactStatusPredicatesAndLinks(t *testing.T) {
	tests := map[string]string{
		"investigating":     "cases.source_type='ineligible_player' AND cases.status='investigating'",
		"awaiting_decision": "cases.source_type='ineligible_player' AND cases.status IN ('decision_proposed','approved')",
		"closed":            "cases.source_type='ineligible_player' AND cases.status='closed'",
	}
	for group, want := range tests {
		if got := ineligibleCaseGroupPredicate(group, "cases"); got != want {
			t.Fatalf("predicate for %s = %q, want %q", group, got, want)
		}
	}
	raw, err := os.ReadFile("admin_ineligible.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		`/admin/cases?group=investigating#cases`,
		`/admin/cases?group=awaiting_decision#cases`,
		`/admin/cases?group=closed#cases`,
		`Review %d row(s) needing attention`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("ineligible dashboard source missing %q", want)
		}
	}
}
