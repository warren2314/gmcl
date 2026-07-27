package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
)

func TestUmpireEquivalentKeysMatchRankingAliases(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{
			name: "Dave Faulkner",
			want: []string{"dave faulkner", "david faulkner"},
		},
		{
			name: "Philip Steven Royle",
			want: []string{"phil royle", "philip royle", "philip steven royle"},
		},
		{
			name: "Unlisted Person",
			want: []string{"unlisted person"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := umpireEquivalentKeys(tt.name); !slices.Equal(got, tt.want) {
				t.Fatalf("umpireEquivalentKeys(%q) = %#v, want %#v", tt.name, got, tt.want)
			}
		})
	}
}

func TestMergeUmpireVariantsCombinesKnownRankingRows(t *testing.T) {
	got := mergeUmpireVariants([]reportUmpire{
		{Name: "Dave Faulkner", Ratings: 1, Good: 1, Score: 3},
		{Name: "David Faulkner", Ratings: 12, Good: 8, Average: 3, Poor: 1, Score: 2.583},
		{Name: "Philip Steven Royle", Ratings: 28, Good: 20, Average: 7, Poor: 1, Score: 2.679},
	})

	byKey := make(map[string]reportUmpire, len(got))
	for _, umpire := range got {
		byKey[umpireCanonicalKey(umpire.Name)] = umpire
	}
	if dave := byKey["dave faulkner"]; dave.Ratings != 13 {
		t.Fatalf("Dave Faulkner ratings = %d, want 13", dave.Ratings)
	}
	if philip := byKey["philip royle"]; philip.Ratings != 28 || philip.Name != "Philip Royle" {
		t.Fatalf("Philip Royle row = %#v, want canonical name with 28 ratings", philip)
	}
}

func TestPremierPanelMatchDefinitionContainsOfficialCompetitions(t *testing.T) {
	for _, competition := range []string{
		"robert hinchliffe premier league",
		"gmcl premier league 2",
		"gmcl championship",
		"gmcl division 1",
		"gmcl saturday premier",
		"gmcl saturday premier 2",
		"gmcl saturday championship",
		"gmcl saturday division 1",
		"derek kay",
		"championship cup",
		"john barrow",
	} {
		if !strings.Contains(premierPanelMatchPredicateSQL, competition) {
			t.Errorf("Premier Panel match definition is missing %q", competition)
		}
	}
}

func TestUmpireMatchScopeFilterSQL(t *testing.T) {
	if got := umpireMatchScopeFilterSQL("m3", "rated_match"); got != "AND rated_match" {
		t.Fatalf("M3 filter = %q", got)
	}
	if got := umpireMatchScopeFilterSQL("other", "rated_match"); got != "AND NOT rated_match" {
		t.Fatalf("other-game filter = %q", got)
	}
	if got := umpireMatchScopeFilterSQL("unexpected", "rated_match"); got != "" {
		t.Fatalf("unexpected scope filter = %q, want no filter", got)
	}
}

func TestLoadUmpireRankingsSeparatesPremierPanelGames(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	const (
		seasonID  int32 = -9101
		weekID    int32 = -9101
		clubID    int32 = -9101
		teamID    int32 = -9101
		captainID int32 = -9101
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM submissions WHERE season_id=$1`, seasonID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM league_fixtures WHERE play_cricket_match_id BETWEEN -910110 AND -910101`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM captains WHERE id=$1`, captainID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM teams WHERE id=$1`, teamID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM clubs WHERE id=$1`, clubID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM weeks WHERE id=$1`, weekID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM seasons WHERE id=$1`, seasonID)
	})

	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO seasons (id,name,start_date,end_date) VALUES ($1,'Umpire scope test season','2026-01-01','2026-12-31')`, []any{seasonID}},
		{`INSERT INTO weeks (id,season_id,week_number,start_date,end_date) VALUES ($1,$2,1,'2026-04-13','2026-04-26')`, []any{weekID, seasonID}},
		{`INSERT INTO clubs (id,name) VALUES ($1,'Umpire Scope Test Club')`, []any{clubID}},
		{`INSERT INTO teams (id,club_id,name,play_cricket_team_id)
		  VALUES ($1,$2,'Umpire Scope Test XI','-9101-test-team')`, []any{teamID, clubID}},
		{`INSERT INTO captains (id,team_id,full_name,email,active_from) VALUES ($1,$2,'Scope Test Captain','scope-test@example.test','2026-01-01')`, []any{captainID, teamID}},
		{`INSERT INTO league_fixtures (play_cricket_match_id,season_id,match_date,home_team_pc_id,payload)
		  VALUES
		    (-910101,$1,'2026-04-18','-9101-test-team','{"competition_name":"Robert Hinchliffe Premier League"}'),
		    (-910102,$1,'2026-04-19','-9101-test-team','{"competition_name":"Derek Kay 1st XI Cup"}'),
		    (-910103,$1,'2026-04-25','-9101-test-team','{"competition_name":"GMCL Saturday Division 5"}'),
		    (-910104,$1,'2026-05-02','-9101-test-team','{"competition_name":"GMCL Premier League 2"}'),
		    (-910105,$1,'2026-05-09','-9101-test-team','{"competition_name":"GMCL Championship"}'),
		    (-910106,$1,'2026-05-16','-9101-test-team','{"competition_name":"GMCL Division 1"}'),
		    (-910107,$1,'2026-05-17','-9101-test-team','{"competition_name":"1st XI Championship Cup"}'),
		    (-910108,$1,'2026-05-24','-9101-test-team','{"competition_name":"John Barrow 1st XI Trophy"}'),
		    (-910109,$1,'2026-05-31','-9101-test-team','{"competition_name":"GMCL Sunday Premier League"}'),
		    (-910110,$1,'2026-06-07','-9101-test-team','{"competition_name":"Derek Kay 1st XI Cup - abandoned test"}')`, []any{seasonID}},
		{`INSERT INTO submissions
		    (season_id,week_id,team_id,captain_id,match_date,pitch_rating,outfield_rating,facilities_rating,play_cricket_match_id,umpire1_type,form_data)
		  VALUES
		    ($1,$2,$3,$4,'2026-04-18',3,3,3,NULL,'club','{"umpire1_name":"Dave Faulkner","umpire1_performance":"Good","import_source":"legacy_csv"}'),
		    ($1,$2,$3,$4,'2026-04-19',3,3,3,-910102,'club','{"umpire1_name":"David Faulkner","umpire1_performance":"Average"}'),
		    ($1,$2,$3,$4,'2026-04-25',3,3,3,-910103,'club','{"umpire1_name":"Dave Faulkner","umpire1_performance":"Poor"}'),
		    ($1,$2,$3,$4,'2026-05-02',3,3,3,-910104,'club','{"umpire1_name":"Philip Steven Royle","umpire1_performance":"Good"}'),
		    ($1,$2,$3,$4,'2026-05-09',3,3,3,-910105,'club','{"umpire1_name":"Dave Faulkner","umpire1_performance":"Good"}'),
		    ($1,$2,$3,$4,'2026-05-16',3,3,3,-910106,'club','{"umpire1_name":"Dave Faulkner","umpire1_performance":"Average"}'),
		    ($1,$2,$3,$4,'2026-05-17',3,3,3,-910107,'club','{"umpire1_name":"Dave Faulkner","umpire1_performance":"Good"}'),
		    ($1,$2,$3,$4,'2026-05-24',3,3,3,-910108,'club','{"umpire1_name":"Dave Faulkner","umpire1_performance":"Good"}'),
		    ($1,$2,$3,$4,'2026-05-31',3,3,3,-910109,'club','{"umpire1_name":"Dave Faulkner","umpire1_performance":"Poor"}'),
		    ($1,$2,$3,$4,'2026-06-07',3,3,3,-910110,'club','{"umpire1_name":"Dave Faulkner","match_outcome":"abandoned"}')`, []any{seasonID, weekID, teamID, captainID}},
	}
	for _, statement := range setup {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("prepare umpire scope fixtures: %v", err)
		}
	}

	server := &Server{DB: pool}
	where := "sub.season_id=$1"
	args := []any{seasonID}
	keyFilter := umpireIncludeSQL(umpireEquivalentKeys("Dave Faulkner"))

	m3 := mergeUmpireVariants(server.loadUmpireRankingsForScope(
		ctx, where, args, 1, "", keyFilter, umpireMatchScopePremierPanel,
	))
	if len(m3) != 1 || m3[0].Ratings != 6 || m3[0].Good != 4 || m3[0].Average != 2 {
		t.Fatalf("M3 rankings = %#v, want Dave Faulkner with all six qualifying competition ratings", m3)
	}

	other := mergeUmpireVariants(server.loadUmpireRankingsForScope(
		ctx, where, args, 1, "", keyFilter, umpireMatchScopeOther,
	))
	if len(other) != 1 || other[0].Ratings != 2 || other[0].Poor != 2 {
		t.Fatalf("other-game rankings = %#v, want two excluded ratings", other)
	}

	audit, auditErr := server.loadPremierUmpireCompetitionAudit(ctx, seasonID)
	if auditErr != nil {
		t.Fatalf("load competition audit: %v", auditErr)
	}
	var m3Audit, otherAudit, exceptionAudit, teamDateAudit, directAudit int64
	for _, row := range audit {
		switch row.Classification {
		case "M3":
			m3Audit += row.Ratings
		case "Other":
			otherAudit += row.Ratings
		case "Exception":
			exceptionAudit += row.Ratings
		}
		switch row.Resolution {
		case "Unique team/date":
			teamDateAudit += row.Ratings
		case "Match ID":
			directAudit += row.Ratings
		}
	}
	if m3Audit != 7 || otherAudit != 2 || exceptionAudit != 0 || teamDateAudit != 1 || directAudit != 8 {
		t.Fatalf("competition audit = %#v; totals M3=%d other=%d exception=%d team/date=%d direct=%d",
			audit, m3Audit, otherAudit, exceptionAudit, teamDateAudit, directAudit)
	}

	scorePage := func(name, scope string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet,
			"/admin/umpires/umpire/scores?season_id=-9101&category=panel&scope="+scope, nil)
		request.SetPathValue("name", name)
		response := httptest.NewRecorder()
		server.handleAdminUmpireScores().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s score page returned %d", name, response.Code)
		}
		return response.Body.String()
	}

	davePage := scorePage("Dave Faulkner", umpireMatchScopePremierPanel)
	for _, want := range []string{
		"Robert Hinchliffe Premier League",
		"Derek Kay 1st XI Cup",
		"GMCL Championship",
		"GMCL Division 1",
		"1st XI Championship Cup",
		"John Barrow 1st XI Trophy",
	} {
		if !strings.Contains(davePage, want) {
			t.Errorf("Dave Faulkner M3 score page is missing %q", want)
		}
	}
	if strings.Contains(davePage, "GMCL Saturday Division 5") {
		t.Error("Dave Faulkner M3 score page included a non-M3 game")
	}
	if strings.Contains(davePage, "abandoned test") {
		t.Error("Dave Faulkner score page counted an abandoned match with no umpire rating")
	}

	philipPage := scorePage("Philip Royle", umpireMatchScopePremierPanel)
	if !strings.Contains(philipPage, "GMCL Premier League 2") ||
		strings.Contains(philipPage, "No ratings found for this umpire") {
		t.Fatal("Philip Royle canonical score page did not find the Philip Steven Royle rating")
	}
}
