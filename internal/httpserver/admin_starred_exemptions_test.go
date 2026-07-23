package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/starred"
)

func sundayStarredBreach() starred.Breach {
	breach := sampleStarredBreach()
	breach.Appearance.PlayingDay = "Sunday"
	breach.Appearance.MatchDate = time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	breach.Appearance.CompetitionType = "League"
	breach.Appearance.CompetitionName = "GMCL Sunday Division 1"
	return breach
}

func TestStarredExemptionInsertPinsRepeatedParameterTypes(t *testing.T) {
	for _, required := range []string{"$14::integer", "$9::text", "NULL::integer"} {
		if !strings.Contains(starredExemptionInsertSQL, required) {
			t.Fatalf("exemption INSERT must contain %q to avoid PostgreSQL 42P08: %s", required, starredExemptionInsertSQL)
		}
	}
}

func TestStarredSundayExemptionEligibilityExcludesSaturdayCupAndT20(t *testing.T) {
	breach := sundayStarredBreach()
	if !starredSundayExemptionEligible(breach) {
		t.Fatal("Sunday league finding should be eligible for exemption review")
	}
	breach.Appearance.PlayingDay = "Saturday"
	if starredSundayExemptionEligible(breach) {
		t.Fatal("Saturday finding must never be eligible")
	}
	breach = sundayStarredBreach()
	breach.Appearance.CompetitionType = "Cup"
	if starredSundayExemptionEligible(breach) {
		t.Fatal("Cup finding must never be eligible")
	}
	breach = sundayStarredBreach()
	breach.Appearance.CompetitionName = "GMCL20 Sunday"
	if starredSundayExemptionEligible(breach) {
		t.Fatal("GMCL20 finding must never be eligible")
	}
}

func TestApprovedSingleMatchExemptionOnlyCoversExactPlayerAndMatch(t *testing.T) {
	breach := sundayStarredBreach()
	exemption := starredExemption{
		ClubKey: "example", PlayerID: 12345, MatchID: breach.Appearance.MatchID,
		ExemptionType: "sunday_single_match", Status: "approved", ValidFrom: breach.Appearance.MatchDate,
	}
	if !exemption.covers(breach) {
		t.Fatal("approved single-match exemption should cover its finding")
	}
	otherMatch := breach
	otherMatch.Appearance.MatchID++
	if exemption.covers(otherMatch) {
		t.Fatal("single-match exemption must not cover another match")
	}
	otherPlayer := breach
	otherPlayer.Appearance.PlayerID++
	if exemption.covers(otherPlayer) {
		t.Fatal("exemption must not cover another player")
	}
	exemption.Status = "pending"
	if exemption.covers(breach) {
		t.Fatal("pending exemption must not suppress a finding")
	}
}

func TestApprovedDevelopmentExemptionCoversOnlyItsDateRange(t *testing.T) {
	breach := sundayStarredBreach()
	validTo := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	exemption := starredExemption{
		ClubKey: "example", PlayerID: 12345, ExemptionType: "sunday_development", Status: "approved",
		ValidFrom: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), ValidTo: &validTo,
	}
	if !exemption.covers(breach) {
		t.Fatal("development exemption should cover a Sunday finding inside its range")
	}
	breach.Appearance.MatchDate = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	if exemption.covers(breach) {
		t.Fatal("development exemption must not cover a finding after its end date")
	}
}

func TestApprovedExemptionLeavesOutstandingQueueAndCanBePrefilled(t *testing.T) {
	breach := sundayStarredBreach()
	exemption := starredExemption{
		ClubKey: "example", PlayerID: 12345, MatchID: breach.Appearance.MatchID,
		ExemptionType: "sunday_single_match", Status: "approved", ValidFrom: breach.Appearance.MatchDate,
	}
	if got := filterStarredBreachesWithoutApprovedExemption([]starred.Breach{breach}, []starredExemption{exemption}); len(got) != 0 {
		t.Fatalf("covered findings should leave the outstanding queue: %#v", got)
	}
	requestURL := starredExemptionRequestURL(breach, 2026)
	for _, want := range []string{"#sunday-exemptions", "exemption_match_id=7458963", "exemption_player_id=12345", "exemption_date=2026-06-14"} {
		if !strings.Contains(requestURL, want) {
			t.Fatalf("prefill URL %q does not contain %q", requestURL, want)
		}
	}
}

func TestApprovedExemptionIsIdentifiedInAuditExport(t *testing.T) {
	breach := sundayStarredBreach()
	exemption := starredExemption{
		ClubKey: "example", PlayerID: 12345, MatchID: breach.Appearance.MatchID,
		ExemptionType: "sunday_single_match", Status: "approved", ValidFrom: breach.Appearance.MatchDate,
	}
	recorder := httptest.NewRecorder()
	writeStarredBreachesCSV(recorder, 2026, []starred.Breach{breach}, nil, []starredExemption{exemption}, nil, nil, true)
	if !strings.Contains(recorder.Body.String(), "Approved Sunday exemption - Single Sunday match") {
		t.Fatalf("audit export does not identify the approved exemption:\n%s", recorder.Body.String())
	}
}
