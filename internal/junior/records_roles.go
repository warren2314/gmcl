package junior

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// recordsAddRole is shared by manual maintenance and reviewed roster apply.
// It never changes an existing dated assignment implicitly.
func recordsAddRole(ctx context.Context, q DBTX, in ContactInput) (int64, bool, error) {
	if in.TeamID != nil {
		var id int64
		if err := q.QueryRow(ctx, `SELECT id FROM junior_teams WHERE id=$1 AND club_id=$2 AND NOT archived`, *in.TeamID, in.ClubID).Scan(&id); err != nil {
			return 0, false, recordsError(err)
		}
	}
	if in.Role == "junior_lead" {
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("junior-lead:%d:%d", in.ClubID, in.SeasonID)); err != nil {
			return 0, false, err
		}
		var overlap bool
		err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_contact_roles WHERE club_id=$1 AND season_id=$2 AND role='junior_lead' AND contact_id<>$3 AND daterange(starts_on,COALESCE(ends_on,'infinity'::date),'[]') && daterange($4::date,COALESCE($5::date,'infinity'::date),'[]'))`, in.ClubID, in.SeasonID, in.ID, in.StartsOn.Format("2006-01-02"), in.EndsOn).Scan(&overlap)
		if err != nil {
			return 0, false, err
		}
		if overlap {
			return 0, false, fmt.Errorf("%w: end the existing Junior Lead assignment before adding a replacement", ErrConflict)
		}
	}
	var id int64
	err := q.QueryRow(ctx, `INSERT INTO junior_contact_roles(contact_id,club_id,season_id,role,age_group,team_id,starts_on,ends_on) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING RETURNING id`, in.ID, in.ClubID, in.SeasonID, in.Role, in.AgeGroup, in.TeamID, in.StartsOn.Format("2006-01-02"), in.EndsOn).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, recordsError(err)
	}
	var endsOn *time.Time
	err = q.QueryRow(ctx, `SELECT id,ends_on FROM junior_contact_roles WHERE contact_id=$1 AND season_id=$2 AND role=$3 AND age_group=$4 AND COALESCE(team_id,0)=COALESCE($5::bigint,0) AND starts_on=$6::date`, in.ID, in.SeasonID, in.Role, in.AgeGroup, in.TeamID, in.StartsOn.Format("2006-01-02")).Scan(&id, &endsOn)
	if err != nil {
		return 0, false, recordsError(err)
	}
	if (endsOn == nil) != (in.EndsOn == nil) || endsOn != nil && endsOn.Format("2006-01-02") != in.EndsOn.Format("2006-01-02") {
		return 0, false, fmt.Errorf("%w: this role already exists with different dates; edit its end date explicitly", ErrConflict)
	}
	return id, false, nil
}
