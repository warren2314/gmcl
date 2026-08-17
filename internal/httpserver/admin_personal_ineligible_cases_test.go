package httpserver

import (
	"net/url"
	"strings"
	"testing"
)

func TestPersonalIneligibleCaseQueriesShareOneCaseCentricContract(t *testing.T) {
	countQuery := personalIneligibleCasesCountQuery()
	listQuery := personalIneligibleCasesListQuery()
	for _, want := range []string{
		"cases.assigned_admin_id=$1",
		"cases.source_type='ineligible_player'",
		"NOT cases.is_test",
		"event.event_type='case_training_designated'",
		"cases.status NOT IN ('published','closed','rejected','withdrawn')",
	} {
		if !strings.Contains(countQuery, want) {
			t.Errorf("personal count query missing %q", want)
		}
		if !strings.Contains(listQuery, want) {
			t.Errorf("personal list query missing %q", want)
		}
	}
	if strings.Contains(countQuery, "sanction_intakes") || strings.Contains(listQuery, "sanction_intakes") {
		t.Fatal("personal case queries must not depend on intake links")
	}
}

func TestParseIneligibleQueueFiltersNormalisesImpossiblePersonalHiddenView(t *testing.T) {
	filter := parseIneligibleQueueFilters(url.Values{
		"scope":    {"mine"},
		"state":    {"new"},
		"worklist": {"deferred"},
	})
	if filter.Scope != "all" {
		t.Fatalf("scope = %q, want all for hidden reports", filter.Scope)
	}
	if filter.Worklist != "deferred" {
		t.Fatalf("worklist = %q, want deferred", filter.Worklist)
	}
}

func TestMineQueryWithoutAdministratorFailsClosed(t *testing.T) {
	query, args := buildIneligibleQueueQueryForAdmin(ineligibleQueueFilters{
		State: "all", Scope: "mine", Worklist: "visible", Sort: "newest",
	}, nil)
	if !strings.Contains(query, "WHERE TRUE AND") || !strings.Contains(query, "FALSE") {
		t.Fatalf("mine query without administrator must fail closed: %s", query)
	}
	if len(args) != 0 {
		t.Fatalf("mine query without administrator args = %#v, want none", args)
	}
}

func TestIneligibleClearFiltersURLPreservesQueueMeaning(t *testing.T) {
	tests := []struct {
		name   string
		filter ineligibleQueueFilters
		want   string
	}{
		{
			name:   "personal cases",
			filter: ineligibleQueueFilters{Scope: "mine", State: "new", Worklist: "deferred"},
			want:   "/admin/ineligible?scope=mine&state=all&worklist=visible",
		},
		{
			name:   "shared triage",
			filter: ineligibleQueueFilters{Scope: "all", State: "all", Worklist: "all"},
			want:   "/admin/ineligible?scope=all&state=open&worklist=visible",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ineligibleClearFiltersURL(test.filter); got != test.want {
				t.Fatalf("clear URL = %q, want %q", got, test.want)
			}
		})
	}
}
