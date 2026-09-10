package httpserver

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestScheduledCardFormSeparatesDefiniteAndConditionalAwards(t *testing.T) {
	start := time.Date(2027, 4, 17, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 9, 25, 0, 0, 0, 0, time.UTC)
	html := adminDecisionEffectsWithSeasonsHTML([]adminDecisionSubject{{id: 42, label: "Whalley Range 1st XI"}}, []adminDecisionSeason{{id: 7, name: "2027", start: start, end: end}})
	for _, want := range []string{`value="scheduled_red"`, "future season (definite)", "Suspended red cards (conditional)", `name="target_season_id"`, `data-start="2027-04-17"`, `name="red_card_count"`, `name="starts_at"`, "1 + 2 + 3 = 6", "A date alone never activates", "Do not add another points adjustment"} {
		if !strings.Contains(html, want) {
			t.Fatalf("form missing %q", want)
		}
	}
	for _, name := range []string{"effect_type", "target_season_id", "red_card_count", "starts_at", "case_subject_id"} {
		if strings.Count(html, `name="`+name+`"`) != 5 {
			t.Fatalf("form rows do not align for %s", name)
		}
	}
	if strings.Contains(html, "No future season is configured") {
		t.Fatal("configured future season shown as missing")
	}
	if !strings.Contains(adminDecisionEffectsHTML(nil), "No future season is configured") {
		t.Fatal("missing future season has no explanation")
	}
}

func TestParseMixedSeasonDecisionPreservesTeamAndRowAlignment(t *testing.T) {
	form := url.Values{
		"effect_type":       {"points_adjustment", "", "points_adjustment", "scheduled_red", "suspended_red"},
		"case_subject_id":   {"11", "", "22", "11", "22"},
		"points":            {"-41", "", "-6", "-6", "99"},
		"target_season_id":  {"7", "", "", "7", "7"},
		"red_card_count":    {"1", "1", "1", "3", "2"},
		"starts_at":         {"2027-04-17", "", "", "2027-04-17", "2027-04-17"},
		"ends_at":           {"", "", "", "", "2027-09-25"},
		"trigger_condition": {"", "", "", "", "A further proven breach during the suspension period"},
	}
	effects, err := parseAdminDecisionEffectsWithSchedule(form)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 4 {
		t.Fatalf("effects=%d", len(effects))
	}
	if *effects[0].Points != -41 || *effects[1].Points != -6 || effects[0].TargetSeasonID != nil || effects[1].TargetSeasonID != nil {
		t.Fatalf("current-season adjustments changed: %#v", effects[:2])
	}
	scheduled := effects[2]
	if *scheduled.CaseSubjectID != 11 || *scheduled.TargetSeasonID != 7 || scheduled.RedCardCount != 3 || scheduled.Points != nil {
		t.Fatalf("scheduled effect misparsed: %#v", scheduled)
	}
	if scheduled.StartsAt.Hour() != 0 || scheduled.StartsAt.Location().String() != "Europe/London" {
		t.Fatalf("effective date is not league-local midnight: %v", scheduled.StartsAt)
	}
	conditional := effects[3]
	if *conditional.CaseSubjectID != 22 || conditional.RedCardCount != 2 || conditional.Trigger == "" || conditional.EndsAt == nil || conditional.Points != nil {
		t.Fatalf("suspension misparsed: %#v", conditional)
	}
}

func TestScheduleParserRejectsAmbiguousOrMalformedAwards(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{"missing season", "target_season_id", ""}, {"invalid season", "target_season_id", "oops"}, {"overflow season", "target_season_id", "999999999999999999"},
		{"missing date", "starts_at", ""}, {"invalid date", "starts_at", "2027-02-30"},
		{"missing count", "red_card_count", ""}, {"fractional count", "red_card_count", "3.5"}, {"negative count", "red_card_count", "-1"}, {"too many", "red_card_count", "101"},
		{"condition on definite award", "trigger_condition", "another breach"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"effect_type": {"scheduled_red"}, "target_season_id": {"7"}, "starts_at": {"2027-04-17"}, "red_card_count": {"3"}}
			form[tc.field] = []string{tc.value}
			if _, err := parseAdminDecisionEffectsWithSchedule(form); err == nil {
				t.Fatal("invalid scheduled award accepted")
			}
		})
	}
	if _, err := parseAdminDecisionEffectsWithSchedule(url.Values{"effect_type": {"suspended_red"}, "target_season_id": {"7"}, "starts_at": {"2027-04-17"}, "red_card_count": {"3"}}); err == nil {
		t.Fatal("future suspension accepted without a condition")
	}
}

func TestScheduledCaseDecisionShowsCountSeasonAndTeam(t *testing.T) {
	points := 6
	start := time.Date(2097, 4, 17, 0, 0, 0, 0, time.UTC)
	html := adminCaseDecisionHTML(adminCaseDecision{Status: "approved"}, []adminCaseEffect{{EffectType: "scheduled_red", Status: "active", Points: &points, StartsAt: &start, TargetSeason: "2097", RedCardCount: 3, TeamName: "Example 1st XI", CountsForTotting: true}})
	for _, want := range []string{"Scheduled", "Applies in season", "2097", "Example 1st XI", "6 points", `>3</dd>`, "17 Apr 2097"} {
		if !strings.Contains(html, want) {
			t.Fatalf("decision missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "League-table points to add") {
		t.Fatal("card deduction rendered as points to add")
	}
}

func TestScheduledTaskCannotBeCompletedEarlyOrResurrected(t *testing.T) {
	starts := time.Date(2027, 4, 17, 0, 0, 0, 0, time.UTC)
	if err := validateScheduledTaskUpdate("complete", &starts, starts.Add(-time.Second), true, true); err == nil {
		t.Fatal("early completion permitted")
	}
	if err := validateScheduledTaskUpdate("complete", &starts, starts, true, true); err != nil {
		t.Fatal(err)
	}
	if err := validateScheduledTaskUpdate("in_progress", &starts, starts.Add(-24*time.Hour), true, true); err != nil {
		t.Fatalf("preparation blocked: %v", err)
	}
	for _, status := range []string{"open", "in_progress", "complete"} {
		if err := validateScheduledTaskUpdate(status, &starts, starts, true, false); err == nil {
			t.Fatalf("withdrawn award task allowed %s", status)
		}
	}
	if err := validateScheduledTaskUpdate("cancelled", &starts, starts, true, false); err != nil {
		t.Fatal(err)
	}
	if err := validateScheduledTaskUpdate("complete", nil, starts, false, false); err != nil {
		t.Fatalf("ordinary task changed: %v", err)
	}
}
