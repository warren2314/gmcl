package junior

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

const RosterUploadLimit = 2 * 1024 * 1024
const RosterRowLimit = 2000

func ParseRosterCSV(data []byte) ([]RosterRow, error) {
	if len(data) == 0 || len(data) > RosterUploadLimit {
		return nil, fmt.Errorf("%w: roster CSV must be at most 2 MB", ErrValidation)
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: read the roster CSV header", ErrValidation)
	}
	columns := map[string]int{}
	allowed := map[string]bool{"club_name": true, "contact_name": true, "email": true, "phone": true, "role": true, "age_group": true, "team_name": true}
	for index, value := range header {
		value = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "\ufeff")))
		value = strings.ReplaceAll(value, " ", "_")
		if !allowed[value] {
			return nil, fmt.Errorf("%w: unrecognised roster column %q", ErrValidation, value)
		}
		if _, ok := columns[value]; ok {
			return nil, fmt.Errorf("%w: duplicate roster column %q", ErrValidation, value)
		}
		columns[value] = index
	}
	if _, ok := columns["club_name"]; !ok {
		return nil, fmt.Errorf("%w: include a club_name column; optional columns are contact_name,email,phone,role,age_group,team_name", ErrValidation)
	}
	var result []RosterRow
	for {
		record, e := reader.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("%w: invalid CSV near row %d", ErrValidation, len(result)+2)
		}
		if len(record) != len(header) {
			return nil, fmt.Errorf("%w: row %d has the wrong number of columns", ErrValidation, len(result)+2)
		}
		if len(result) >= RosterRowLimit {
			return nil, fmt.Errorf("%w: split rosters larger than 2,000 rows", ErrValidation)
		}
		field := func(name string) string {
			index, ok := columns[name]
			if !ok {
				return ""
			}
			return strings.TrimSpace(record[index])
		}
		row := RosterRow{Number: len(result) + 1, ClubName: field("club_name"), ContactName: field("contact_name"), Email: field("email"), Phone: field("phone"), Role: field("role"), AgeGroup: strings.ToUpper(field("age_group")), TeamName: field("team_name")}
		if row.ClubName == "" || len(row.ClubName) > 200 || len(row.TeamName) > 120 || strings.IndexFunc(row.ClubName+row.TeamName, unicode.IsControl) >= 0 {
			row.Problem = "A valid club name and bounded team name are required."
		}
		if row.ContactName != "" {
			in := ContactInput{FullName: row.ContactName, Email: row.Email, Phone: row.Phone}
			if err = validateRecordContact(&in); err != nil {
				row.Problem = err.Error()
			}
			row.Email = in.Email
			if row.Role != "junior_lead" && row.Role != "safeguarding_officer" && row.Role != "coach" {
				row.Problem = "Choose junior_lead, safeguarding_officer or coach for an adult contact."
			}
			if row.Role == "coach" {
				switch row.AgeGroup {
				case "U9", "U11", "U13", "U15", "U18":
				default:
					row.Problem = "A coach needs U9, U11, U13, U15 or U18."
				}
			} else if row.AgeGroup != "" || row.TeamName != "" {
				row.Problem = "Junior Lead and Safeguarding Officer assignments are club-wide."
			}
			if row.Role == "junior_lead" && row.Email == "" {
				row.Problem = "A Junior Lead needs an email address."
			}
		} else if row.Email != "" || row.Phone != "" || row.Role != "" || row.AgeGroup != "" {
			row.Problem = "Contact fields need an adult contact_name."
		}
		result = append(result, row)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: the roster contains no data rows", ErrValidation)
	}
	return result, nil
}

func (s *Service) PreviewRoster(ctx context.Context, p Principal, seasonID int64, filename string, data []byte) (RosterPreview, error) {
	var result RosterPreview
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, 0, true); err != nil {
		return result, err
	}
	if p.AdminID == 0 {
		return result, ErrForbidden
	}
	if _, err = recordsSeason(ctx, tx, seasonID); err != nil {
		return result, err
	}
	rows, err := ParseRosterCSV(data)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	filename = path.Base(strings.ReplaceAll(filename, "\\", "/"))
	if len(filename) > 200 {
		filename = filename[:200]
	}
	encoded, _ := json.Marshal(rows)
	err = tx.QueryRow(ctx, `INSERT INTO junior_roster_imports(season_id,source_name,sha256,source_rows,created_by_admin_id) VALUES($1,$2,$3,$4::jsonb,$5) ON CONFLICT(season_id,sha256) DO UPDATE SET sha256=EXCLUDED.sha256 RETURNING id`, seasonID, filename, hash, string(encoded), p.AdminID).Scan(&result.ID)
	if err != nil {
		return result, err
	}
	if err = Audit(ctx, tx, p, 0, "roster_import", result.ID, "roster_previewed", map[string]any{"season_id": seasonID, "sha256": hash, "rows": len(rows)}); err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return s.GetRosterPreview(ctx, p, result.ID)
}

func loadRosterPreview(ctx context.Context, q DBTX, id int64, lock bool) (RosterPreview, error) {
	var result RosterPreview
	var source, review, applied []byte
	sql := `SELECT id,season_id,source_name,sha256,state,source_rows,review_decisions,apply_result FROM junior_roster_imports WHERE id=$1`
	if lock {
		sql += ` FOR UPDATE`
	}
	err := q.QueryRow(ctx, sql, id).Scan(&result.ID, &result.SeasonID, &result.SourceName, &result.SHA256, &result.State, &source, &review, &applied)
	if err != nil {
		return result, recordsError(err)
	}
	if err = json.Unmarshal(source, &result.Rows); err != nil {
		return result, err
	}
	if err = json.Unmarshal(review, &result.Review); err != nil {
		return result, err
	}
	if err = json.Unmarshal(applied, &result.Result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) GetRosterPreview(ctx context.Context, p Principal, id int64) (RosterPreview, error) {
	var result RosterPreview
	if err := Authorize(ctx, s.DB, p, 0, false); err != nil {
		return result, err
	}
	if p.AdminID == 0 {
		return result, ErrForbidden
	}
	result, err := loadRosterPreview(ctx, s.DB, id, false)
	if err != nil {
		return result, err
	}
	rows, err := s.DB.Query(ctx, `SELECT c.id,c.name,COALESCE(c.short_name,''),COALESCE(part.state,'not_entered') FROM clubs c LEFT JOIN junior_club_seasons part ON part.club_id=c.id AND part.season_id=$1 ORDER BY c.name,c.id`, result.SeasonID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var club ClubSummary
		if err = rows.Scan(&club.ID, &club.Name, &club.ShortName, &club.State); err != nil {
			rows.Close()
			return result, err
		}
		result.Clubs = append(result.Clubs, club)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for index := range result.Rows {
		row := &result.Rows[index]
		for _, club := range result.Clubs {
			if strings.EqualFold(strings.TrimSpace(club.Name), strings.TrimSpace(row.ClubName)) {
				row.CandidateClubIDs = append(row.CandidateClubIDs, club.ID)
			}
		}
		if len(row.CandidateClubIDs) == 1 {
			row.SuggestedClubID = row.CandidateClubIDs[0]
		}
	}
	rows, err = s.DB.Query(ctx, `SELECT id,club_id,full_name,email,phone,archived FROM junior_contacts WHERE NOT archived ORDER BY club_id,full_name,id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item Contact
		if err = rows.Scan(&item.ID, &item.ClubID, &item.FullName, &item.Email, &item.Phone, &item.Archived); err != nil {
			rows.Close()
			return result, err
		}
		result.Contacts = append(result.Contacts, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = s.DB.Query(ctx, `SELECT id,club_id,name,archived FROM junior_teams WHERE NOT archived ORDER BY club_id,name,id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item Team
		if err = rows.Scan(&item.ID, &item.ClubID, &item.Name, &item.Archived); err != nil {
			rows.Close()
			return result, err
		}
		result.Teams = append(result.Teams, item)
	}
	err = rows.Err()
	rows.Close()
	return result, err
}

func (s *Service) ReviewRoster(ctx context.Context, p Principal, id int64, mappings []RosterMapping) (RosterPreview, error) {
	var result RosterPreview
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, 0, true); err != nil {
		return result, err
	}
	if p.AdminID == 0 {
		return result, ErrForbidden
	}
	result, err = loadRosterPreview(ctx, tx, id, true)
	if err != nil {
		return result, err
	}
	if result.State == "applied" {
		return result, fmt.Errorf("%w: this roster was already applied", ErrConflict)
	}
	if len(mappings) != len(result.Rows) {
		return result, fmt.Errorf("%w: review every source row", ErrValidation)
	}
	seen := map[int]bool{}
	for _, mapping := range mappings {
		if mapping.RowNumber < 1 || mapping.RowNumber > len(result.Rows) || seen[mapping.RowNumber] {
			return result, fmt.Errorf("%w: invalid roster row mapping", ErrValidation)
		}
		seen[mapping.RowNumber] = true
		if mapping.Skip {
			if strings.TrimSpace(mapping.Reason) == "" || len(mapping.Reason) > 300 {
				return result, fmt.Errorf("%w: give a reason of at most 300 characters for each skipped row", ErrValidation)
			}
			continue
		}
		if mapping.ClubID < 1 {
			return result, fmt.Errorf("%w: map row %d to an existing club", ErrValidation, mapping.RowNumber)
		}
		if result.Rows[mapping.RowNumber-1].Problem != "" {
			return result, fmt.Errorf("%w: correct the CSV or explicitly skip row %d", ErrValidation, mapping.RowNumber)
		}
		var clubID int32
		if err = tx.QueryRow(ctx, `SELECT id FROM clubs WHERE id=$1`, mapping.ClubID).Scan(&clubID); err != nil {
			return result, recordsError(err)
		}
	}
	encoded, _ := json.Marshal(mappings)
	if _, err = tx.Exec(ctx, `UPDATE junior_roster_imports SET state='reviewed',review_decisions=$2::jsonb WHERE id=$1`, id, string(encoded)); err != nil {
		return result, err
	}
	if err = Audit(ctx, tx, p, 0, "roster_import", id, "roster_club_mappings_reviewed", mappings); err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return s.GetRosterPreview(ctx, p, id)
}

// ApplyRoster commits only the explicitly reviewed club mapping. Existing adult
// identities and team names are reused unchanged; creation requires a separate
// affirmative choice. The locked import makes retries idempotent.
func (s *Service) ApplyRoster(ctx context.Context, p Principal, id int64, mappings []RosterMapping) (RosterApplyResult, error) {
	var result RosterApplyResult
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, 0, true); err != nil {
		return result, err
	}
	if p.AdminID == 0 {
		return result, ErrForbidden
	}
	preview, err := loadRosterPreview(ctx, tx, id, true)
	if err != nil {
		return result, err
	}
	if preview.State == "applied" {
		return preview.Result, nil
	}
	if preview.State != "reviewed" || len(mappings) != len(preview.Rows) {
		return result, fmt.Errorf("%w: review every club mapping before applying this roster", ErrValidation)
	}
	reviewed := map[int]RosterMapping{}
	for _, mapping := range preview.Review {
		reviewed[mapping.RowNumber] = mapping
	}
	seen := map[int]bool{}
	approvedActivation := map[int32]bool{}
	for _, mapping := range mappings {
		prior, ok := reviewed[mapping.RowNumber]
		if !ok || seen[mapping.RowNumber] || prior.Skip != mapping.Skip || (!mapping.Skip && prior.ClubID != mapping.ClubID) {
			return result, fmt.Errorf("%w: club mappings changed; review them again", ErrConflict)
		}
		seen[mapping.RowNumber] = true
		if !mapping.Skip && mapping.ActivateParticipation {
			approvedActivation[mapping.ClubID] = true
		}
	}
	contacts := map[string]int64{}
	teams := map[string]int64{}
	participation := map[int32]bool{}
	location, err := time.LoadLocation("Europe/London")
	if err != nil {
		return result, err
	}
	today := time.Now().In(location).Format("2006-01-02")
	startsOn, _ := time.Parse("2006-01-02", today)
	for _, mapping := range mappings {
		prior, ok := reviewed[mapping.RowNumber]
		if !ok || prior.Skip != mapping.Skip || (!mapping.Skip && prior.ClubID != mapping.ClubID) {
			return result, fmt.Errorf("%w: club mappings changed; review them again", ErrConflict)
		}
		row := preview.Rows[mapping.RowNumber-1]
		if mapping.Skip {
			if strings.TrimSpace(prior.Reason) == "" {
				return result, fmt.Errorf("%w: a skipped row needs a reason", ErrValidation)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO junior_roster_applied_rows(import_id,row_number,skipped,reason) VALUES($1,$2,true,$3)`, id, row.Number, prior.Reason); err != nil {
				return result, err
			}
			result.RowsSkipped++
			continue
		}
		if row.Problem != "" {
			return result, fmt.Errorf("%w: invalid source row %d must be skipped", ErrValidation, row.Number)
		}
		if !participation[mapping.ClubID] {
			// Serialize against normal participation updates and another import.
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("junior-participation:%d:%d", mapping.ClubID, preview.SeasonID)); err != nil {
				return result, err
			}
			var state string
			err = tx.QueryRow(ctx, `SELECT state FROM junior_club_seasons WHERE club_id=$1 AND season_id=$2 FOR UPDATE`, mapping.ClubID, preview.SeasonID).Scan(&state)
			if err != nil && err != pgx.ErrNoRows {
				return result, err
			}
			if state == "withdrawn" && !approvedActivation[mapping.ClubID] {
				return result, fmt.Errorf("%w: explicitly approve reactivating withdrawn club on row %d", ErrConflict, row.Number)
			}
			if state != "active" {
				provenance, _ := json.Marshal(map[string]any{"source": "reviewed_roster", "import_id": id, "source_name": preview.SourceName, "sha256": preview.SHA256, "row": row.Number})
				if _, err = tx.Exec(ctx, `INSERT INTO junior_club_seasons(club_id,season_id,state,provenance) VALUES($1,$2,'active',$3::jsonb) ON CONFLICT(club_id,season_id) DO UPDATE SET state='active',provenance=EXCLUDED.provenance,updated_at=now()`, mapping.ClubID, preview.SeasonID, string(provenance)); err != nil {
					return result, recordsError(err)
				}
				result.ClubsActivated++
				if err = Audit(ctx, tx, p, mapping.ClubID, "club_participation", preview.SeasonID, "roster_participation_applied", map[string]any{"import_id": id, "row": row.Number}); err != nil {
					return result, err
				}
			}
			participation[mapping.ClubID] = true
		}
		var contactID, teamID, roleID int64
		if row.TeamName != "" {
			if mapping.CreateTeam == (mapping.TeamID > 0) {
				return result, fmt.Errorf("%w: explicitly reuse or create the team on row %d", ErrValidation, row.Number)
			}
			if mapping.TeamID > 0 {
				err = tx.QueryRow(ctx, `SELECT id FROM junior_teams WHERE id=$1 AND club_id=$2 AND NOT archived`, mapping.TeamID, mapping.ClubID).Scan(&teamID)
				if err != nil {
					return result, recordsError(err)
				}
			} else {
				key := fmt.Sprintf("%d:%s", mapping.ClubID, strings.ToLower(strings.TrimSpace(row.TeamName)))
				teamID = teams[key]
				if teamID == 0 {
					err = tx.QueryRow(ctx, `INSERT INTO junior_teams(club_id,name) VALUES($1,$2) RETURNING id`, mapping.ClubID, row.TeamName).Scan(&teamID)
					if err != nil {
						return result, recordsError(err)
					}
					teams[key] = teamID
					result.TeamsCreated++
					if err = Audit(ctx, tx, p, mapping.ClubID, "team", teamID, "roster_team_created", map[string]any{"import_id": id, "row": row.Number}); err != nil {
						return result, err
					}
				}
			}
		} else if mapping.TeamID > 0 || mapping.CreateTeam {
			return result, fmt.Errorf("%w: row %d has no source team", ErrValidation, row.Number)
		}
		if row.ContactName != "" {
			if mapping.CreateContact == (mapping.ContactID > 0) {
				return result, fmt.Errorf("%w: explicitly reuse or create the adult contact on row %d", ErrValidation, row.Number)
			}
			in := ContactInput{ClubID: mapping.ClubID, SeasonID: preview.SeasonID, FullName: row.ContactName, Email: row.Email, Phone: row.Phone, Role: row.Role, AgeGroup: row.AgeGroup, StartsOn: startsOn}
			if teamID > 0 {
				in.TeamID = &teamID
			}
			if mapping.ContactID > 0 {
				err = tx.QueryRow(ctx, `SELECT id,full_name,email,phone FROM junior_contacts WHERE id=$1 AND club_id=$2 AND NOT archived`, mapping.ContactID, mapping.ClubID).Scan(&contactID, &in.FullName, &in.Email, &in.Phone)
				if err != nil {
					return result, recordsError(err)
				}
			} else {
				key := fmt.Sprintf("%d:%s:%s:%s", mapping.ClubID, row.ContactName, row.Email, row.Phone)
				contactID = contacts[key]
				if contactID == 0 {
					if err = validateRecordContact(&in); err != nil {
						return result, err
					}
					err = tx.QueryRow(ctx, `INSERT INTO junior_contacts(club_id,full_name,email,phone) VALUES($1,$2,$3,$4) RETURNING id`, mapping.ClubID, in.FullName, in.Email, in.Phone).Scan(&contactID)
					if err != nil {
						return result, recordsError(err)
					}
					contacts[key] = contactID
					result.ContactsCreated++
					if err = Audit(ctx, tx, p, mapping.ClubID, "contact", contactID, "roster_contact_created", map[string]any{"import_id": id, "row": row.Number}); err != nil {
						return result, err
					}
				}
			}
			in.ID = contactID
			if err = validateRecordContact(&in); err != nil {
				return result, err
			}
			var added bool
			roleID, added, err = recordsAddRole(ctx, tx, in)
			if err != nil {
				return result, err
			}
			if added {
				result.RolesAdded++
				if err = Audit(ctx, tx, p, mapping.ClubID, "contact_role", roleID, "roster_role_added", map[string]any{"import_id": id, "row": row.Number}); err != nil {
					return result, err
				}
			}
		} else if mapping.ContactID > 0 || mapping.CreateContact {
			return result, fmt.Errorf("%w: row %d has no source adult contact", ErrValidation, row.Number)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO junior_roster_applied_rows(import_id,row_number,club_id,contact_id,team_id,role_id) VALUES($1,$2,$3,NULLIF($4,0),NULLIF($5,0),NULLIF($6,0))`, id, row.Number, mapping.ClubID, contactID, teamID, roleID); err != nil {
			return result, err
		}
		result.RowsApplied++
	}
	encoded, _ := json.Marshal(result)
	decisions, _ := json.Marshal(mappings)
	if _, err = tx.Exec(ctx, `UPDATE junior_roster_imports SET state='applied',apply_result=$2::jsonb,review_decisions=$3::jsonb,applied_at=now() WHERE id=$1`, id, string(encoded), string(decisions)); err != nil {
		return result, err
	}
	if err = Audit(ctx, tx, p, 0, "roster_import", id, "roster_applied", result); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
