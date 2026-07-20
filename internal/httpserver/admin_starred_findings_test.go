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

func TestWriteStarredBreachesCSVIncludesReviewStatusAndScorecard(t *testing.T) {
	breach := sampleStarredBreach()
	breach.StarredName = "Alexander Player"
	recorder := httptest.NewRecorder()
	writeStarredBreachesCSV(recorder, 2026, []starred.Breach{breach}, map[string]starredFindingState{
		starredFindingKey(breach): {ID: 42, Status: "accepted"},
	}, nil, nil)
	if contentDisposition := recorder.Header().Get("Content-Disposition"); !strings.Contains(contentDisposition, "starred-player-breaches-2026-all-dates.csv") {
		t.Fatalf("unexpected content disposition: %q", contentDisposition)
	}
	for _, want := range []string{"Review status", "Accepted / closed", "Alexander Player", "match_id=7458963"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("CSV does not contain %q:\n%s", want, recorder.Body.String())
		}
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

func TestStarredFindingActionsRequireSeparateDraftAndApproval(t *testing.T) {
	breach := sampleStarredBreach()
	pending := starredFindingActionsHTML(breach, starredFindingState{}, "token", 2026, "2026-05-01", "2026-06-30")
	for _, want := range []string{"Accept / close", "Not accepted", "/findings/escalate", `name="breach_from" value="2026-05-01"`, `name="breach_to" value="2026-06-30"`} {
		if !strings.Contains(pending, want) {
			t.Fatalf("pending actions do not contain %q: %s", want, pending)
		}
	}
	draft := starredFindingActionsHTML(breach, starredFindingState{ID: 42, Status: "draft"}, "token", 2026, "", "")
	if !strings.Contains(draft, "/findings/42") || strings.Contains(draft, "approve") {
		t.Fatalf("draft state should link to review without approving inline: %s", draft)
	}
	breach.NeedsExemptionReview = true
	junior := starredFindingActionsHTML(breach, starredFindingState{}, "token", 2026, "", "")
	for _, want := range []string{"Accept junior exemption", "close every finding for this player"} {
		if !strings.Contains(junior, want) {
			t.Fatalf("junior actions do not contain %q: %s", want, junior)
		}
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
	states := map[string]starredFindingState{
		starredFindingKey(accepted): {Status: "accepted"},
		starredFindingKey(draft):    {Status: "draft"},
		starredFindingKey(sent):     {Status: "sent"},
	}
	got := filterOutstandingStarredBreaches([]starred.Breach{accepted, draft, sent, pending}, states)
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
		"breach_from": {"2026-05-01"},
		"breach_to":   {"2026-06-30"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/starred-players/findings/accept", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	redirectStarredFinding(recorder, request, 2026, "closed", "", &breach)
	anchor := starredBreachGroupAnchor(starredBreachDay(breach), starredDivisionLabel(breach.Appearance.CompetitionName, breach.Appearance.CompetitionType))
	location := recorder.Header().Get("Location")
	for _, want := range []string{"breach_from=2026-05-01", "breach_to=2026-06-30", "breach_return=" + anchor, "#" + anchor} {
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
	for _, want := range []string{"Dear Casey Captain", "Rule 3.5", "List A", "Potential offence", "23 May 2026", "7458963", "Alex Player", "Scorecard evidence"} {
		if !strings.Contains(body, want) {
			t.Fatalf("letter does not contain %q:\n%s", want, body)
		}
	}
}
