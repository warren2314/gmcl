package junior

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// One row describes one physically possible simultaneous layout. Rows are
// alternatives, never independent age limits that can be added together.
type GroundLayout map[string]int
type Ground struct {
	ID, Revision                    int64
	Name, Address, Postcode, Source string
	Layouts                         []GroundLayout
	Archived, RulesConfirmed        bool
}

var GroundAges = []string{"U11", "U13", "U15", "U18"}
var groundPostcode = regexp.MustCompile(`^(GIR0AA|[A-Z]{1,2}[0-9][0-9A-Z]?[0-9][A-Z]{2})$`)

func GroundLayoutsText(layouts []GroundLayout) string {
	alternatives := []string{}
	for _, layout := range layouts {
		parts := []string{}
		for _, age := range GroundAges {
			if layout[age] > 0 {
				parts = append(parts, fmt.Sprintf("%d × %s", layout[age], age))
			}
		}
		alternatives = append(alternatives, strings.Join(parts, " + "))
	}
	return strings.Join(alternatives, " OR ")
}
func validateGround(g *Ground) error {
	g.Name = strings.TrimSpace(g.Name)
	g.Address = strings.Join(strings.Fields(g.Address), " ")
	g.Source = strings.TrimSpace(g.Source)
	p := strings.ToUpper(strings.Join(strings.Fields(g.Postcode), ""))
	if !groundPostcode.MatchString(p) {
		return fixtureValidation("enter a postcode in UK format; verify the actual address separately")
	}
	g.Postcode = p[:len(p)-3] + " " + p[len(p)-3:]
	if g.Name == "" || len(g.Name) > 200 || g.Address == "" || len(g.Address) > 500 || g.Source == "" || len(g.Source) > 500 || strings.IndexFunc(g.Name+g.Address+g.Source, unicode.IsControl) >= 0 || !g.RulesConfirmed {
		return fixtureValidation("provide a ground name, address, verification/source note and confirmed layouts")
	}
	if len(g.Layouts) < 1 || len(g.Layouts) > 8 {
		return fixtureValidation("add one to eight permitted layouts")
	}
	for _, layout := range g.Layouts {
		total := 0
		for age, n := range layout {
			valid := false
			for _, a := range GroundAges {
				if a == age {
					valid = true
				}
			}
			if !valid || n < 0 || n > 10 {
				return fixtureValidation("layouts support U11/U13/U15/U18 with zero to ten games per age")
			}
			total += n
		}
		if total < 1 || total > 10 {
			return fixtureValidation("each layout needs one to ten simultaneous games")
		}
	}
	return nil
}
func loadGrounds(ctx context.Context, q DBTX) ([]Ground, error) {
	rows, err := q.Query(ctx, `SELECT id,revision,name,address,postcode,source,layouts,archived FROM junior_grounds ORDER BY name,address,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Ground{}
	for rows.Next() {
		var g Ground
		var raw []byte
		if err = rows.Scan(&g.ID, &g.Revision, &g.Name, &g.Address, &g.Postcode, &g.Source, &raw, &g.Archived); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &g.Layouts); err != nil {
			return nil, err
		}
		items = append(items, g)
	}
	return items, rows.Err()
}
func (s *Service) Grounds(ctx context.Context, p Principal) ([]Ground, error) {
	if p.AdminID < 1 || p.ReadOnly {
		return nil, ErrForbidden
	}
	if err := Authorize(ctx, s.DB, p, 0, false); err != nil {
		return nil, err
	}
	return loadGrounds(ctx, s.DB)
}
func (s *Service) SaveGround(ctx context.Context, p Principal, g Ground) (int64, error) {
	if p.AdminID < 1 || p.ReadOnly {
		return 0, ErrForbidden
	}
	if err := validateGround(&g); err != nil {
		return 0, err
	}
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	raw, err := json.Marshal(g.Layouts)
	if err != nil {
		return 0, err
	}
	if g.ID == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO junior_grounds(name,address,postcode,source,layouts,archived,updated_by_admin_id) VALUES($1,$2,$3,$4,$5::jsonb,$6,$7) RETURNING id`, g.Name, g.Address, g.Postcode, g.Source, string(raw), g.Archived, p.AdminID).Scan(&g.ID)
	} else {
		var id int64
		err = tx.QueryRow(ctx, `UPDATE junior_grounds SET name=$3,address=$4,postcode=$5,source=$6,layouts=$7::jsonb,archived=$8,revision=revision+1,updated_by_admin_id=$9,updated_at=now() WHERE id=$1 AND revision=$2 RETURNING id`, g.ID, g.Revision, g.Name, g.Address, g.Postcode, g.Source, string(raw), g.Archived, p.AdminID).Scan(&id)
		if err == pgx.ErrNoRows {
			return 0, ErrConflict
		}
	}
	if err != nil {
		return 0, recordsError(err)
	}
	// A prepared publication must be reviewed again after venue rules change.
	_, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id IN(SELECT competition_id FROM junior_fixtures WHERE ground_id=$1 UNION SELECT c.id FROM junior_competitions c JOIN junior_publication_fixtures f ON f.publication_id=c.current_publication_id WHERE f.ground_id=$1 OR EXISTS(SELECT 1 FROM junior_publication_ground_links l WHERE l.publication_id=f.publication_id AND l.fixture_id=f.fixture_id AND l.ground_id=$1) UNION SELECT competition_id FROM junior_fixture_constraints fc WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF(fc.settings->'grounds','null'::jsonb),'[]'::jsonb)) x WHERE x->>'ground_id'=$1::bigint::text))`, g.ID)
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, p, 0, "ground", g.ID, "ground_saved", g); err != nil {
		return 0, err
	}
	return g.ID, tx.Commit(ctx)
}
func (s *Service) LinkPublishedGround(ctx context.Context, p Principal, publication, fixture, ground int64) error {
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_publication_fixtures f JOIN junior_competitions c ON c.current_publication_id=f.publication_id JOIN junior_grounds g ON g.id=$3 AND NOT g.archived WHERE f.publication_id=$1 AND f.fixture_id=$2 AND f.ground_id IS NULL)`, publication, fixture, ground).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	var current int64
	err = tx.QueryRow(ctx, `SELECT ground_id FROM junior_publication_ground_links WHERE publication_id=$1 AND fixture_id=$2 ORDER BY id DESC LIMIT 1`, publication, fixture).Scan(&current)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if current == ground {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO junior_publication_ground_links(publication_id,fixture_id,ground_id,linked_by_admin_id) VALUES($1,$2,$3,$4)`, publication, fixture, ground, p.AdminID); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, 0, "fixture", fixture, "published_ground_linked", map[string]int64{"publication_id": publication, "ground_id": ground}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE current_publication_id=$1`, publication); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func hydrateFixtureGrounds(ctx context.Context, q DBTX, fixtures []Fixture) error {
	grounds, err := loadGrounds(ctx, q)
	if err != nil {
		return err
	}
	byID := map[int64]Ground{}
	for _, g := range grounds {
		byID[g.ID] = g
	}
	for i := range fixtures {
		f := &fixtures[i]
		f.RequiresSavedGround = len(grounds) > 0 && f.AgeGroup != "U9"
		if f.GroundID == 0 {
			continue
		}
		g, ok := byID[f.GroundID]
		if !ok {
			return ErrNotFound
		}
		f.GroundLayouts = g.Layouts
		f.GroundArchived = g.Archived
		f.GroundRevision = g.Revision
		if f.PublicationID == 0 || f.GroundPostcode == "" {
			f.GroundPostcode = g.Postcode
			f.GroundAddress = g.Address
		}
		if f.PublicationID == 0 {
			f.GroundName = g.Name
		}
	}
	return nil
}
func resolveGroundConstraints(ctx context.Context, q DBTX, c Competition, f *FixtureConstraints) error {
	grounds, err := loadGrounds(ctx, q)
	if err != nil {
		return err
	}
	byID := map[int64]Ground{}
	for _, g := range grounds {
		byID[g.ID] = g
	}
	for i := range f.Grounds {
		g := &f.Grounds[i]
		if g.GroundID == 0 {
			if len(grounds) > 0 && c.AgeGroup != "U9" {
				return fixtureValidation("choose a saved ground for every home venue")
			}
			continue
		}
		record, ok := byID[g.GroundID]
		if !ok || record.Archived {
			return fixtureValidation("choose an active saved ground")
		}
		g.Name = record.Name
		g.Capacity = 1
	}
	return nil
}
func loadOtherJuniorBookings(ctx context.Context, q DBTX, competition int64) ([]Fixture, error) {
	published, err := loadFixtureRows(ctx, q, 0, true, 0)
	if err != nil {
		return nil, err
	}
	drafts, err := loadFixtureRows(ctx, q, 0, false, 0)
	if err != nil {
		return nil, err
	}
	out := []Fixture{}
	for _, f := range append(published, drafts...) {
		if f.CompetitionID != competition {
			out = append(out, f)
		}
	}
	return out, nil
}
func (s *Service) GroundBookings(ctx context.Context, p Principal, season int64) ([]Fixture, []FixtureIssue, error) {
	if p.AdminID < 1 || p.ReadOnly {
		return nil, nil, ErrForbidden
	}
	if err := Authorize(ctx, s.DB, p, 0, false); err != nil {
		return nil, nil, err
	}
	all, err := loadOtherJuniorBookings(ctx, s.DB, 0)
	if err != nil {
		return nil, nil, err
	}
	out := []Fixture{}
	otherSeasons := []Fixture{}
	seen := map[string]bool{}
	for _, f := range all {
		if season > 0 && f.SeasonID != season {
			otherSeasons = append(otherSeasons, f)
			continue
		}
		key := fmt.Sprintf("%d/%d/%s/%s/%d/%s/%d", f.CompetitionID, f.ID, f.MatchDate, f.StartTime, f.GroundID, f.GroundName, f.DurationMinutes)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.MatchDate+a.StartTime+a.GroundName < b.MatchDate+b.StartTime+b.GroundName
	})
	return out, CheckGroundLayouts(out, otherSeasons), nil
}
