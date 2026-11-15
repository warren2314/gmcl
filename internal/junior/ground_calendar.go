package junior

import (
	"context"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
	"unicode"
)

// External bookings reserve the whole physical ground. They are kept outside
// junior competitions and never become junior results or team appearances.
type ExternalGroundBooking struct {
	ID, GroundID                                  int64
	Title, Category, MatchDate, StartTime, Source string
	DurationMinutes                               int
}

func validateExternalBooking(b *ExternalGroundBooking) error {
	b.Title = strings.TrimSpace(b.Title)
	b.Source = strings.TrimSpace(b.Source)
	_, err := fixtureDay(b.MatchDate)
	if err != nil || !validFixtureTime(b.StartTime) || b.GroundID < 1 || b.Title == "" || len(b.Title) > 200 || b.Source == "" || len(b.Source) > 500 || strings.IndexFunc(b.Title+b.Source, unicode.IsControl) >= 0 {
		return fixtureValidation("provide a saved ground, date, valid time, title and source")
	}
	if b.Category != "women" && b.Category != "senior" && b.Category != "other" {
		return fixtureValidation("choose women, senior or other")
	}
	clock, _ := time.Parse("15:04", b.StartTime)
	b.StartTime = clock.Format("15:04")
	minute := clock.Hour()*60 + clock.Minute()
	if b.DurationMinutes < 1 || minute+b.DurationMinutes > 1440 {
		return fixtureValidation("booking duration must end on the selected day")
	}
	return nil
}
func (s *Service) SaveExternalGroundBooking(ctx context.Context, p Principal, b ExternalGroundBooking) (int64, error) {
	if err := validateExternalBooking(&b); err != nil {
		return 0, err
	}
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_grounds WHERE id=$1 AND NOT archived)`, b.GroundID).Scan(&valid); err != nil {
		return 0, err
	}
	if !valid {
		return 0, fixtureValidation("choose an active saved ground")
	}
	err = tx.QueryRow(ctx, `INSERT INTO junior_external_ground_bookings(ground_id,title,category,match_date,start_time,duration_minutes,source,created_by_admin_id) VALUES($1,$2,$3,$4::date,$5,$6,$7,$8) RETURNING id`, b.GroundID, b.Title, b.Category, b.MatchDate, b.StartTime, b.DurationMinutes, b.Source, p.AdminID).Scan(&b.ID)
	if err != nil {
		return 0, err
	}
	// Invalidate reviewed publication revisions under the same fixture lock used
	// by publication, so a booking added after preview cannot bypass review.
	if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id IN (SELECT competition_id FROM junior_fixtures WHERE ground_id=$1 AND NOT retired AND match_date=$2::date)`, b.GroundID, b.MatchDate); err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, p, 0, "external_ground_booking", b.ID, "created", b); err != nil {
		return 0, err
	}
	return b.ID, tx.Commit(ctx)
}
func (s *Service) CancelExternalGroundBooking(ctx context.Context, p Principal, id int64) error {
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var b ExternalGroundBooking
	err = tx.QueryRow(ctx, `UPDATE junior_external_ground_bookings SET cancelled=true WHERE id=$1 AND NOT cancelled RETURNING id,ground_id,match_date::text`, id).Scan(&b.ID, &b.GroundID, &b.MatchDate)
	if err == pgx.ErrNoRows {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id IN (SELECT competition_id FROM junior_fixtures WHERE ground_id=$1 AND NOT retired AND match_date=$2::date)`, b.GroundID, b.MatchDate); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, 0, "external_ground_booking", id, "cancelled", b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func loadExternalGroundBookings(ctx context.Context, q DBTX) ([]Fixture, error) {
	rows, err := q.Query(ctx, `SELECT b.id,b.ground_id,b.title,b.category,b.match_date::text,b.start_time,b.duration_minutes,g.name,g.postcode,g.address,b.source FROM junior_external_ground_bookings b JOIN junior_grounds g ON g.id=b.ground_id WHERE NOT b.cancelled ORDER BY b.match_date,b.start_time,b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Fixture{}
	for rows.Next() {
		var f Fixture
		var title, category string
		if err = rows.Scan(&f.ID, &f.GroundID, &title, &category, &f.MatchDate, &f.StartTime, &f.DurationMinutes, &f.GroundName, &f.GroundPostcode, &f.GroundAddress, &f.ExternalSource); err != nil {
			return nil, err
		}
		f.CompetitionID = -f.ID // disjoint from junior competition IDs and fixture identities
		f.CompetitionName = category + ": " + title
		f.AgeGroup = "External"
		f.BookingState = "external"
		out = append(out, f)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err = hydrateFixtureGrounds(ctx, q, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GroundDay includes every recorded booking for the venue, irrespective of
// season. DivisionDay deliberately includes all grounds, so moves can be assessed.
func GroundDay(fixtures []Fixture, date string, groundID int64) []Fixture {
	out := []Fixture{}
	for _, f := range fixtures {
		if f.MatchDate == date && f.GroundID == groundID {
			out = append(out, f)
		}
	}
	return out
}
func DivisionDay(fixtures []Fixture, date string, competitionID int64) []Fixture {
	out := []Fixture{}
	for _, f := range fixtures {
		if f.MatchDate == date && f.CompetitionID == competitionID {
			out = append(out, f)
		}
	}
	return out
}
