package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIneligibleTodayLaneShowsOnlyActionableCounts(t *testing.T) {
	counts := ineligibleDashboardCounts{
		NewIntakes: 2, AwaitingSelection: 3, HiddenReports: 198, ActiveCases: 4,
		ResponsesDue: 3, ResponsesOverdue: 5, RecentReplies: 1, AwaitingDecision: 0,
		AwaitingDenverSignoff: 7, PlayCricketPointsTasks: 2, DeliveryExceptions: 0, ClosedCases: 8,
	}
	tiles := ineligibleTodayTiles(counts)
	if len(tiles) != 4 {
		t.Fatalf("team queue shows %d tiles, want the four actionable ones", len(tiles))
	}
	for _, tile := range tiles {
		if strings.Contains(tile.Label, "Denver") || strings.Contains(tile.Label, "Hidden") || strings.Contains(tile.Label, "Closed") {
			t.Fatalf("%q is a running total, not work to pick up, so it must not sit in the team queue", tile.Label)
		}
	}
	output := httptest.NewRecorder()
	writeIneligibleTodayLane(output, counts)
	html := output.Body.String()
	for _, want := range []string{"Team queue", "Reports to review", "Club replies to read", "Responses overdue", "Cases under investigation"} {
		if !strings.Contains(html, want) {
			t.Fatalf("team queue is missing %q", want)
		}
	}
	if strings.Contains(html, "198") {
		t.Fatal("hidden-report total leaked into the team queue")
	}
}

func TestIneligibleTotalsShowDenverAsOneTile(t *testing.T) {
	counts := ineligibleDashboardCounts{AwaitingDenverSignoff: 7, PlayCricketPointsTasks: 2}
	denverTiles := 0
	for _, tile := range ineligibleTotalsTiles(counts) {
		if strings.Contains(tile.Label, "Denver") {
			denverTiles++
			if tile.Count != 7 {
				t.Fatalf("Denver tile counts %d cases, want 7", tile.Count)
			}
			if !strings.Contains(tile.Note, "League points awaiting Denver") || !strings.Contains(tile.Note, "2") {
				t.Fatalf("Denver tile note %q does not carry the open league-points work", tile.Note)
			}
			if tile.NoteHref != "/admin/cases/tasks" {
				t.Fatalf("Denver league-points note links to %q, want the task list", tile.NoteHref)
			}
		}
	}
	if denverTiles != 1 {
		t.Fatalf("dashboard renders %d Denver tiles, want exactly one", denverTiles)
	}
}

func TestIneligibleDenverTileHidesEmptyPointsNote(t *testing.T) {
	for _, tile := range ineligibleTotalsTiles(ineligibleDashboardCounts{AwaitingDenverSignoff: 1}) {
		if strings.Contains(tile.Label, "Denver") && tile.Note != "" {
			t.Fatalf("Denver tile still shows a league-points note (%q) when none is open", tile.Note)
		}
	}
}

func TestIneligibleTilesEscapeAndLinkNotes(t *testing.T) {
	output := httptest.NewRecorder()
	writeIneligibleTiles(output, []ineligibleWorkTile{{
		Label: "Cases <all>", Count: 3, Href: "/admin/cases?group=closed#cases", Accent: "border-success",
		Note: "League points awaiting Denver: 2", NoteHref: "/admin/cases/tasks",
	}})
	html := output.Body.String()
	if strings.Contains(html, "Cases <all>") {
		t.Fatal("tile label was not escaped")
	}
	if !strings.Contains(html, `href="/admin/cases/tasks">League points awaiting Denver: 2</a>`) {
		t.Fatalf("tile note is not a link to its own list: %s", html)
	}
}

func TestIneligibleNextReportActionNamesTheStartingPoint(t *testing.T) {
	href, label := ineligibleNextReportAction(1176)
	if href != "/admin/ineligible/1176" || label != "Open next selected report" {
		t.Fatalf("next report action = %q/%q", href, label)
	}
	href, label = ineligibleNextReportAction(0)
	if !strings.HasPrefix(href, "/admin/ineligible?") || label != "View reports" {
		t.Fatalf("empty queue action = %q/%q", href, label)
	}
}

func TestIneligibleDashboardPutsPersonalWorkBeforeTeamQueue(t *testing.T) {
	source := ineligibleDashboardSource(t)
	personal := strings.Index(source, "s.writeAdminPersonalWork(w, r)")
	team := strings.Index(source, "writeIneligibleTodayLane(w, counts)")
	totals := strings.Index(source, "writeIneligibleTotalsLane(w, counts)")
	if personal < 0 || team < 0 || totals < 0 {
		t.Fatalf("dashboard is missing a work lane: personal=%d team=%d totals=%d", personal, team, totals)
	}
	if !(personal < team && team < totals) {
		t.Fatalf("dashboard order is wrong: personal=%d team=%d totals=%d", personal, team, totals)
	}
	if !strings.Contains(source, `href="/admin/cases/close-batch">Close historic cases`) {
		t.Fatal("dashboard does not offer the bulk close route")
	}
}
