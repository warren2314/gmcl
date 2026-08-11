package httpserver

import (
	"net/http/httptest"
	"os"
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

func TestApprovedJuniorSeasonExemptionCoversFutureFindingsOnlyThisSeason(t *testing.T) {
	breach := sampleStarredBreach()
	breach.Appearance.SeasonYear = 2026
	breach.Appearance.MatchDate = time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	breach.Appearance.CompetitionType = "League"
	breach.Appearance.PlayingDay = "Saturday"
	validTo := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	exemption := starredExemption{
		SeasonYear: 2026, ClubKey: breach.Appearance.ClubKey, PlayerID: breach.Appearance.PlayerID,
		PlayerKey: breach.Appearance.PlayerKey, ExemptionType: starredExemptionTypeJuniorSeason, Status: "approved",
		ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), ValidTo: &validTo,
	}
	if !exemption.covers(breach) {
		t.Fatal("approved junior exemption should cover a later Saturday finding in the same season")
	}
	cup := breach
	cup.Appearance.MatchID++
	cup.Appearance.CompetitionType = "Cup"
	if !exemption.covers(cup) {
		t.Fatal("junior exemption should cover every starred finding for the player, including Cup findings")
	}
	if got := filterStarredBreachesWithoutApprovedExemption([]starred.Breach{breach, cup}, []starredExemption{exemption}); len(got) != 0 {
		t.Fatalf("newly imported findings should remain exempted: %#v", got)
	}
	afterSeason := breach
	afterSeason.Appearance.MatchDate = validTo.AddDate(0, 0, 1)
	if exemption.covers(afterSeason) {
		t.Fatal("junior exemption must not cover a finding after its configured season end")
	}
	exemption.ValidTo = nil
	nextSeason := breach
	nextSeason.Appearance.SeasonYear = 2027
	nextSeason.Appearance.MatchDate = time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	if exemption.covers(nextSeason) {
		t.Fatal("junior exemption must not carry into the next season")
	}
	otherClub := breach
	otherClub.Appearance.ClubKey = "another"
	if exemption.covers(otherClub) {
		t.Fatal("junior exemption must not cover another club")
	}
	differentPlayer := breach
	differentPlayer.Appearance.PlayerID++
	if exemption.covers(differentPlayer) {
		t.Fatal("junior exemption must not fall back to a matching name when both player IDs differ")
	}
	missingID := breach
	missingID.Appearance.PlayerID = 0
	if !exemption.covers(missingID) {
		t.Fatal("junior exemption should fall back to the stable player key when an appearance has no player ID")
	}
	exemption.Status = "pending"
	if exemption.covers(breach) {
		t.Fatal("pending junior exemption must not suppress a finding")
	}
	exemption.Status = "approved"
	revokedAt := time.Now()
	exemption.RevokedAt = &revokedAt
	if exemption.covers(breach) {
		t.Fatal("revoked junior exemption must not suppress a finding")
	}
}

func TestApprovedJuniorExemptionIsIdentifiedInAuditExport(t *testing.T) {
	breach := sampleStarredBreach()
	breach.Appearance.SeasonYear = 2026
	validTo := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	exemption := starredExemption{
		SeasonYear: 2026, ClubKey: breach.Appearance.ClubKey, PlayerID: breach.Appearance.PlayerID,
		PlayerKey: breach.Appearance.PlayerKey, ExemptionType: starredExemptionTypeJuniorSeason, Status: "approved",
		ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), ValidTo: &validTo,
	}
	recorder := httptest.NewRecorder()
	writeStarredBreachesCSV(recorder, 2026, []starred.Breach{breach}, nil, []starredExemption{exemption}, nil, nil, true)
	if !strings.Contains(recorder.Body.String(), "Approved junior exemption - season-long") {
		t.Fatalf("audit export does not identify the approved junior exemption:\n%s", recorder.Body.String())
	}
}

func TestStarredExemptionIdentityKeyPrefersPlayerID(t *testing.T) {
	if got := starredExemptionIdentityKey(12345, "alexplayer"); got != "id:12345" {
		t.Fatalf("ID-backed identity=%q", got)
	}
	if got := starredExemptionIdentityKey(0, "alexplayer"); got != "key:alexplayer" {
		t.Fatalf("name-backed identity=%q", got)
	}
	if starredExemptionIdentityKey(12345, "alexplayer") == starredExemptionIdentityKey(67890, "alexplayer") {
		t.Fatal("same-named players with different Play-Cricket IDs must have different exemption identities")
	}
}

func TestStarredJuniorExemptionPlayerIDRequiresUnambiguousIdentity(t *testing.T) {
	tests := []struct {
		name                   string
		selectedID, knownCount int64
		onlyKnownID, wantID    int64
		wantErr                bool
	}{
		{name: "selected ID remains authoritative", selectedID: 201, knownCount: 2, onlyKnownID: 201, wantID: 201},
		{name: "no known ID remains name-backed"},
		{name: "sole known ID is adopted", knownCount: 1, onlyKnownID: 301, wantID: 301},
		{name: "multiple known IDs require matching", knownCount: 2, onlyKnownID: 301, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := starredJuniorExemptionPlayerID(test.selectedID, test.knownCount, test.onlyKnownID)
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, test.wantErr)
			}
			if got != test.wantID {
				t.Fatalf("player ID=%d, want %d", got, test.wantID)
			}
		})
	}
}

func TestJuniorExemptionFallbackCannotReplaceDifferentKnownPlayerID(t *testing.T) {
	raw, err := os.ReadFile("admin_starred_exemptions.go")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, want := range []string{
		"HAVING COUNT(*)=1",
		"BOOL_AND(candidate.play_cricket_player_id IS NULL)",
		"FROM starred_appearances appearance",
		"FROM starred_finding_reviews finding",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("junior exemption fallback lacks the known-player-ID guard %q", want)
		}
	}
}

func TestJuniorExemptionMigrationBackfillsAuditedAndAcceptedTaggedApprovals(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0071_starred_junior_season_exemptions.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, want := range []string{
		"uq_starred_junior_exemptions_season_identity",
		"exemption_type = 'junior_season'",
		"log.entity_id = f.id",
		`log.metadata @> '{"junior_bulk": true}'::jsonb`,
		"f.status = 'accepted'",
		"FROM unnest(p.tags) tag",
		"WHEN related_ids.id_count = 1",
		"FROM starred_appearances appearance",
		"AND related_ids.id_count > 1 AS ambiguous_identity",
		"s.is_archived = FALSE",
		"ON CONFLICT (season_year, club_key, identity_key)",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("junior exemption migration does not contain %q", want)
		}
	}
}
