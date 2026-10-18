package junior

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type queuedWorkflowMessage struct {
	ClubID                int32
	SeasonID, RecordID    int64
	Kind, RecordType, Key string
}

// Validate the business record again inside the recipient-claim transaction.
// Preparation is not permission to send obsolete reminders or official data.
// Locks serialize this claim with submission, correction and season closure;
// SMTP runs after the claim commits, without holding business locks over I/O.
func validateQueuedWorkflowMessage(ctx context.Context, q DBTX, message queuedWorkflowMessage) error {
	switch message.Kind {
	case "form_reminder":
		if message.RecordType != "form_assignment" {
			return fmt.Errorf("%w: this reminder has an invalid assignment reference", ErrConflict)
		}
		var status string
		var seasonID int64
		var clubID int32
		err := q.QueryRow(ctx, `SELECT a.status,f.season_id,a.club_id FROM junior_form_assignments a JOIN junior_forms f ON f.id=a.form_id WHERE a.id=$1 FOR UPDATE OF a`, message.RecordID).Scan(&status, &seasonID, &clubID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: this reminder's assignment is no longer available", ErrConflict)
		}
		if err != nil {
			return err
		}
		if seasonID != message.SeasonID || clubID != message.ClubID || (status != "assigned" && status != "returned") {
			return fmt.Errorf("%w: this reminder is no longer outstanding; the club has responded or the assignment was superseded", ErrConflict)
		}
	case "fixture_publication":
		if message.RecordType != "fixture_publication" {
			return fmt.Errorf("%w: this fixture message has an invalid publication reference", ErrConflict)
		}
		var current, seasonID int64
		err := q.QueryRow(ctx, `SELECT COALESCE(c.current_publication_id,0),c.season_id FROM junior_fixture_publications p JOIN junior_competitions c ON c.id=p.competition_id WHERE p.id=$1 FOR UPDATE OF c`, message.RecordID).Scan(&current, &seasonID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: this fixture publication is no longer available", ErrConflict)
		}
		if err != nil {
			return err
		}
		if current != message.RecordID || seasonID != message.SeasonID {
			return fmt.Errorf("%w: these fixtures were superseded; use the latest official publication", ErrConflict)
		}
	case "concession_information":
		if message.RecordType != "concession" {
			return fmt.Errorf("%w: this information request has an invalid concession reference", ErrConflict)
		}
		var state string
		var revision, seasonID int64
		var homeClub, awayClub int32
		err := q.QueryRow(ctx, `SELECT candidate.state,candidate.revision,c.season_id,h.club_id,a.club_id FROM junior_concession_candidates candidate JOIN junior_fixtures f ON f.id=candidate.fixture_id JOIN junior_competitions c ON c.id=f.competition_id JOIN junior_teams h ON h.id=f.home_team_id JOIN junior_teams a ON a.id=f.away_team_id WHERE candidate.id=$1 FOR UPDATE OF candidate`, message.RecordID).Scan(&state, &revision, &seasonID, &homeClub, &awayClub)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: this concession review is no longer available", ErrConflict)
		}
		if err != nil {
			return err
		}
		expected := fmt.Sprintf("concession:%d:revision:%d:club:%d", message.RecordID, revision, message.ClubID)
		if state != "information_requested" || seasonID != message.SeasonID || (message.ClubID != homeClub && message.ClubID != awayClub) || message.Key != expected {
			return fmt.Errorf("%w: this information request is no longer current; review the latest concession evidence and decision", ErrConflict)
		}
	default:
		return nil // Manual correspondence keeps its reviewed snapshot semantics.
	}
	var open bool
	err := q.QueryRow(ctx, `SELECT state='open' FROM junior_seasons WHERE id=$1 FOR SHARE`, message.SeasonID).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: this workflow message's season is no longer available", ErrConflict)
	}
	if err != nil {
		return err
	}
	if !open {
		return fmt.Errorf("%w: this junior season is closed; the prepared workflow message will not be sent", ErrConflict)
	}
	return nil
}
