package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/starred"
)

func sampleStarredBreach() starred.Breach {
	return starred.Breach{
		ListType: "A",
		Appearance: starred.Appearance{
			MatchID:         7458963,
			MatchDate:       time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC),
			ClubName:        "Example CC",
			ClubKey:         "example",
			TeamName:        "Example CC 2nd XI",
			CompetitionName: "Division Two",
			PlayingDay:      "Saturday",
			PlayerID:        12345,
			PlayerName:      "Alex Player",
			PlayerKey:       "alexplayer",
		},
	}
}

func TestStarredBreachDateRangeIsInclusiveAndAllowsOneSidedFilters(t *testing.T) {
	request := httptest.NewRequest("GET", "/admin/starred-players?breach_from=2026-05-23&breach_to=2026-06-01", nil)
	from, to, err := parseStarredBreachDateRange(request)
	if err != nil {
		t.Fatal(err)
	}
	breaches := make([]starred.Breach, 3)
	for index, date := range []time.Time{
		time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	} {
		breaches[index] = sampleStarredBreach()
		breaches[index].Appearance.MatchDate = date
	}
	filtered := filterStarredBreachesByDate(breaches, from, to)
	if len(filtered) != 2 || filtered[0].Appearance.MatchDate.Day() != 23 || filtered[1].Appearance.MatchDate.Day() != 1 {
		t.Fatalf("filtered breaches=%#v", filtered)
	}

	request = httptest.NewRequest("GET", "/admin/starred-players?breach_to=2026-05-23", nil)
	from, to, err = parseStarredBreachDateRange(request)
	if err != nil || from != nil || to == nil || len(filterStarredBreachesByDate(breaches, from, to)) != 2 {
		t.Fatalf("one-sided date filter failed: from=%v to=%v err=%v", from, to, err)
	}
}

func TestParseStarredBreachRecentWindowIsValidatedAndLimited(t *testing.T) {
	request := httptest.NewRequest("GET", "/admin/starred-players", nil)
	if recent, err := parseStarredBreachRecentWindow(request); err != nil || recent != 0 {
		t.Fatalf("empty breach_recent should be treated as 0: recent=%d err=%v", recent, err)
	}
	request = httptest.NewRequest("GET", "/admin/starred-players?breach_recent=3", nil)
	if recent, err := parseStarredBreachRecentWindow(request); err != nil || recent != 3 {
		t.Fatalf("breach_recent=3 should parse: recent=%d err=%v", recent, err)
	}
	request = httptest.NewRequest("GET", "/admin/starred-players?breach_recent=0", nil)
	if _, err := parseStarredBreachRecentWindow(request); err == nil {
		t.Fatalf("breach_recent=0 should be rejected")
	}
	request = httptest.NewRequest("GET", "/admin/starred-players?breach_recent=abc", nil)
	if _, err := parseStarredBreachRecentWindow(request); err == nil {
		t.Fatalf("non-numeric breach_recent should be rejected")
	}
	request = httptest.NewRequest("GET", "/admin/starred-players?breach_recent=999", nil)
	if _, err := parseStarredBreachRecentWindow(request); err == nil {
		t.Fatalf("too-large breach_recent should be rejected")
	}
}

func TestFilterStarredBreachesByRecentAppearancesKeepsRecentPerIdentity(t *testing.T) {
	makeBreach := func(id int64, playerID int64, clubKey string, day int) starred.Breach {
		breach := sampleStarredBreach()
		breach.Appearance.MatchID = id
		breach.Appearance.PlayerID = playerID
		breach.Appearance.ClubKey = clubKey
		breach.Appearance.ClubName = strings.ToUpper(clubKey) + " Club"
		breach.Appearance.MatchDate = time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC)
		return breach
	}
	breaches := []starred.Breach{
		makeBreach(1001, 9001, "alpha", 30),
		makeBreach(1002, 9002, "beta", 29),
		makeBreach(1003, 9001, "alpha", 20),
		makeBreach(1004, 9003, "gamma", 15),
		makeBreach(1005, 9001, "alpha", 10),
		makeBreach(1006, 9002, "beta", 5),
	}
	filtered := filterStarredBreachesByRecentAppearances(breaches, 2)
	if len(filtered) != 5 {
		t.Fatalf("filtered breaches=%d want 5: %#v", len(filtered), filtered)
	}
	for _, wantID := range []int64{1001, 1002, 1003, 1004, 1006} {
		found := false
		for _, got := range filtered {
			if got.Appearance.MatchID == wantID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected breach %d in filtered set: %#v", wantID, filtered)
		}
	}
}

func TestFilterOutstandingStarredBreachesRemovesAcceptedFindings(t *testing.T) {
	accepted := sampleStarredBreach()
	outstanding := sampleStarredBreach()
	outstanding.Appearance.MatchID++
	draft := sampleStarredBreach()
	draft.Appearance.MatchID += 2

	filtered := filterOutstandingStarredBreaches([]starred.Breach{accepted, outstanding, draft}, map[string]starredFindingState{
		starredFindingKey(accepted): {ID: 41, Status: "accepted"},
		starredFindingKey(draft):    {ID: 42, Status: "draft"},
	})

	if len(filtered) != 2 {
		t.Fatalf("outstanding breaches=%d want 2: %#v", len(filtered), filtered)
	}
	if filtered[0].Appearance.MatchID != outstanding.Appearance.MatchID || filtered[1].Appearance.MatchID != draft.Appearance.MatchID {
		t.Fatalf("unexpected outstanding breaches: %#v", filtered)
	}
}

func TestWriteStarredBreachesCSVIncludesReviewStatusAndScorecard(t *testing.T) {
	breach := sampleStarredBreach()
	breach.StarredName = "Alexander Player"
	recorder := httptest.NewRecorder()
	writeStarredBreachesCSV(recorder, 2026, []starred.Breach{breach}, map[string]starredFindingState{
		starredFindingKey(breach): {ID: 42, Status: "accepted"},
	}, nil, nil, nil, true)
	if contentDisposition := recorder.Header().Get("Content-Disposition"); !strings.Contains(contentDisposition, "starred-player-breaches-2026-including-closed-all-dates.csv") {
		t.Fatalf("unexpected content disposition: %q", contentDisposition)
	}
	for _, want := range []string{"Review status", "Accepted / closed", "Alexander Player", "match_id=7458963"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("CSV does not contain %q:\n%s", want, recorder.Body.String())
		}
	}
}

func TestStarredBreachExportExcludesClosedUnlessRequested(t *testing.T) {
	accepted := sampleStarredBreach()
	outstanding := sampleStarredBreach()
	outstanding.Appearance.MatchID++
	sent := sampleStarredBreach()
	sent.Appearance.MatchID += 2
	breaches := []starred.Breach{accepted, outstanding, sent}
	states := map[string]starredFindingState{
		starredFindingKey(accepted): {ID: 41, Status: "accepted"},
		starredFindingKey(sent):     {ID: 42, Status: "sent"},
	}
	defaultRows := starredBreachExportRows(breaches, states, false)
	if len(defaultRows) != 1 || defaultRows[0].Appearance.MatchID != outstanding.Appearance.MatchID {
		t.Fatalf("default export rows=%#v", defaultRows)
	}
	includingClosed := starredBreachExportRows(breaches, states, true)
	if len(includingClosed) != 3 {
		t.Fatalf("including-closed export rows=%d want 3", len(includingClosed))
	}
}

func TestGroupStarredBreachesByDayAndDivisionOrder(t *testing.T) {
	makeBreach := func(day, competition string) starred.Breach {
		breach := sampleStarredBreach()
		breach.Appearance.PlayingDay = day
		breach.Appearance.CompetitionName = competition
		return breach
	}
	groups := groupStarredBreaches([]starred.Breach{
		makeBreach("Sunday", "GMCL Sunday Division 1"),
		makeBreach("Saturday", "GMCL Championship"),
		makeBreach("Saturday", "GMCL Premier League 2"),
		makeBreach("Saturday", "GMCL Premier League 1"),
		makeBreach("Saturday", "GMCL Premier League 1"),
	})
	want := []string{
		"Saturday — Premier 1",
		"Saturday — Premier 2",
		"Saturday — Championship",
		"Sunday — Division 1",
	}
	if len(groups) != len(want) {
		t.Fatalf("groups=%d want %d: %#v", len(groups), len(want), groups)
	}
	for index, group := range groups {
		got := group.Day + " — " + group.Division
		if got != want[index] {
			t.Errorf("group %d=%q want %q", index, got, want[index])
		}
	}
	if len(groups[0].Breaches) != 2 {
		t.Fatalf("Premier 1 findings=%d want 2", len(groups[0].Breaches))
	}
	if starredDivisionRank("Division 10") <= starredDivisionRank("Division 2") {
		t.Fatal("numbered divisions must be sorted numerically")
	}
}

func TestStarredFindingKeyIsStableAndPlayerSpecific(t *testing.T) {
	breach := sampleStarredBreach()
	first := starredFindingKey(breach)
	if first == "" || first != starredFindingKey(breach) {
		t.Fatalf("finding key is not stable: %q", first)
	}
	breach.Appearance.PlayerID++
	if first == starredFindingKey(breach) {
		t.Fatal("different players in a match must have different finding keys")
	}
}

func TestStarredFindingActionsCreateCaseWithoutReplacingLegacyDrafts(t *testing.T) {
	breach := sampleStarredBreach()
	pending := starredFindingActionsHTML(breach, starredFindingState{}, "token", 2026, "2026-05-01", "2026-06-30", "")
	for _, want := range []string{"Accept / close", "Create ineligible-player case", "/findings/create-case", "No email will be sent", `name="breach_from" value="2026-05-01"`, `name="breach_to" value="2026-06-30"`, `name="breach_recent"`} {
		if !strings.Contains(pending, want) {
			t.Fatalf("pending actions do not contain %q: %s", want, pending)
		}
	}
	if strings.Contains(pending, "draft letter") || strings.Contains(pending, "/findings/escalate") {
		t.Fatalf("new findings must not create a legacy letter draft: %s", pending)
	}
	draft := starredFindingActionsHTML(breach, starredFindingState{ID: 42, Status: "draft"}, "token", 2026, "", "", "3")
	if !strings.Contains(draft, "/findings/42") || strings.Contains(draft, "approve") {
		t.Fatalf("draft state should link to review without approving inline: %s", draft)
	}
	caseAction := starredFindingActionsHTML(breach, starredFindingState{
		CaseID: 73, CaseReference: "GMCL-2026-001073", CaseStatus: "investigating",
	}, "token", 2026, "", "", "3")
	if !strings.Contains(caseAction, `/admin/cases/73`) || !strings.Contains(caseAction, "GMCL-2026-001073") || strings.Contains(caseAction, "<form") {
		t.Fatalf("linked finding should open its existing case: %s", caseAction)
	}
	breach.NeedsExemptionReview = true
	junior := starredFindingActionsHTML(breach, starredFindingState{}, "token", 2026, "", "", "")
	for _, want := range []string{"Accept junior exemption", "rest of this season", "close every current finding for this player"} {
		if !strings.Contains(junior, want) {
			t.Fatalf("junior actions do not contain %q: %s", want, junior)
		}
	}
	sunday := breach
	sunday.Appearance.PlayingDay = "Sunday"
	sunday.Appearance.CompetitionType = "League"
	sunday.Appearance.CompetitionName = "GMCL Sunday Division 1"
	if actions := starredFindingActionsHTML(sunday, starredFindingState{}, "token", 2026, "", "", ""); !strings.Contains(actions, "Record Sunday exemption") {
		t.Fatalf("Sunday league finding should offer the exemption workflow: %s", actions)
	}
	if strings.Contains(pending, "Record Sunday exemption") {
		t.Fatalf("Saturday finding must not offer a Sunday exemption: %s", pending)
	}
}

func TestOutstandingStarredBreachesExcludeAcceptedAndSent(t *testing.T) {
	accepted := sampleStarredBreach()
	draft := sampleStarredBreach()
	draft.Appearance.MatchID++
	sent := sampleStarredBreach()
	sent.Appearance.MatchID += 2
	pending := sampleStarredBreach()
	pending.Appearance.MatchID += 3
	linked := sampleStarredBreach()
	linked.Appearance.MatchID += 4
	states := map[string]starredFindingState{
		starredFindingKey(accepted): {Status: "accepted"},
		starredFindingKey(draft):    {Status: "draft"},
		starredFindingKey(sent):     {Status: "sent"},
		starredFindingKey(linked):   {CaseID: 55, CaseReference: "GMCL-2026-001055", CaseStatus: "investigating"},
	}
	got := filterOutstandingStarredBreaches([]starred.Breach{accepted, draft, sent, pending, linked}, states)
	if len(got) != 2 || got[0].Appearance.MatchID != draft.Appearance.MatchID || got[1].Appearance.MatchID != pending.Appearance.MatchID {
		t.Fatalf("outstanding breaches=%#v", got)
	}
}

func TestJuniorAcceptanceSelectsEveryFindingForPlayer(t *testing.T) {
	selected := sampleStarredBreach()
	selected.NeedsExemptionReview = true
	secondMatch := selected
	secondMatch.Appearance.MatchID++
	secondMatch.NeedsExemptionReview = false
	secondList := secondMatch
	secondList.Appearance.MatchID++
	secondList.ListType = "B"
	otherPlayer := selected
	otherPlayer.Appearance.MatchID += 3
	otherPlayer.Appearance.PlayerID++
	otherClub := selected
	otherClub.Appearance.MatchID += 4
	otherClub.Appearance.ClubKey = "another"
	got := juniorTaggedIdentityBreaches([]starred.Breach{selected, secondMatch, secondList, otherPlayer, otherClub}, selected)
	if len(got) != 3 {
		t.Fatalf("junior matches=%d want 3: %#v", len(got), got)
	}
	selected.NeedsExemptionReview = false
	if got := juniorTaggedIdentityBreaches([]starred.Breach{selected, secondMatch}, selected); len(got) != 1 || got[0].Appearance.MatchID != selected.Appearance.MatchID {
		t.Fatalf("ordinary acceptance should close only the selected finding: %#v", got)
	}
}

func TestStarredBreachGroupAnchorIsStableAndValid(t *testing.T) {
	first := starredBreachGroupAnchor("Saturday", "Division 2")
	if first != starredBreachGroupAnchor(" saturday ", "division 2") || !validStarredBreachGroupAnchor(first) {
		t.Fatalf("group anchor is not stable and valid: %q", first)
	}
	if first == starredBreachGroupAnchor("Saturday", "Division 3") || validStarredBreachGroupAnchor("potential-breaches") {
		t.Fatal("group anchors must be specific and strictly validated")
	}
}

func TestRedirectStarredFindingReturnsToExactGroupAndKeepsDates(t *testing.T) {
	breach := sampleStarredBreach()
	form := url.Values{
		"breach_from":   {"2026-05-01"},
		"breach_to":     {"2026-06-30"},
		"breach_recent": {"3"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/starred-players/findings/accept", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	redirectStarredFinding(recorder, request, 2026, "closed", "", &breach)
	anchor := starredBreachGroupAnchor(starredBreachDay(breach), starredDivisionLabel(breach.Appearance.CompetitionName, breach.Appearance.CompetitionType))
	location := recorder.Header().Get("Location")
	for _, want := range []string{"breach_from=2026-05-01", "breach_to=2026-06-30", "breach_recent=3", "breach_return=" + anchor, "#" + anchor} {
		if !strings.Contains(location, want) {
			t.Fatalf("redirect location %q does not contain %q", location, want)
		}
	}
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("redirect status=%d want %d", recorder.Code, http.StatusSeeOther)
	}
}

func TestStarredFindingDraftIncludesOffenceAndScorecardEvidence(t *testing.T) {
	breach := sampleStarredBreach()
	subject, body := starredFindingDraft(breach, starredCaptain{Name: "Casey Captain"}, "Example CC — 2nd XI:\n- Alex Player")
	if !strings.Contains(subject, "Example CC") {
		t.Fatalf("subject does not identify the club: %s", subject)
	}
	for _, want := range []string{"Dear Casey Captain", "Rule 3.5", "List A", "Potential offence", "23 May 2026", "7458963", "Alex Player", "Scorecard evidence", "docs.google.com/forms", "gtrmcrcricket.co.uk/pages/rules-3-5", "review should be reconsidered"} {
		if !strings.Contains(body, want) {
			t.Fatalf("letter does not contain %q:\n%s", want, body)
		}
	}
}
