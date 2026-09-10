package httpserver

import (
	"strings"
	"testing"
	"time"
)

func TestScheduledTaskRejectionUsesLeagueCivilDate(t *testing.T) {
	// PostgreSQL returns the UTC instant for 1 April at midnight in London.
	start := time.Date(2027, time.March, 31, 23, 0, 0, 0, time.UTC)
	err := validateScheduledTaskUpdate("complete", &start, start.Add(-time.Second), true, true)
	if err == nil || !strings.Contains(err.Error(), "01 Apr 2027") || strings.Contains(err.Error(), "31 Mar") {
		t.Fatalf("task rejection has wrong league date: %v", err)
	}
	if err := validateScheduledTaskUpdate("complete", &start, start, true, true); err != nil {
		t.Fatalf("completion blocked at league midnight: %v", err)
	}
}

func TestAdminDecisionDatesUseLeagueCivilDates(t *testing.T) {
	start := time.Date(2027, time.March, 31, 23, 0, 0, 0, time.UTC)
	end := time.Date(2027, time.September, 29, 23, 0, 0, 0, time.UTC)
	html := adminCaseDecisionHTML(adminCaseDecision{Status: "approved"}, []adminCaseEffect{{
		EffectType: "suspended_red", Status: "suspended", StartsAt: &start, EndsAt: &end,
		TargetSeason: "2027", RedCardCount: 3,
	}})
	for _, want := range []string{"01 Apr 2027", "30 Sep 2027"} {
		if !strings.Contains(html, want) {
			t.Fatalf("decision missing league date %q: %s", want, html)
		}
	}
	for _, wrong := range []string{"31 Mar 2027", "29 Sep 2027"} {
		if strings.Contains(html, wrong) {
			t.Fatalf("decision shows UTC date %q: %s", wrong, html)
		}
	}
}
