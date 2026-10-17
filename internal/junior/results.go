package junior

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

type FixtureResultInput struct {
	FixtureID         int64
	Outcome, Evidence string
}
type ResultImportSummary struct {
	ImportID                 int64
	Observations, Candidates int
	Replayed                 bool
}
type ResultObservation struct {
	ID, FixtureID, ImportID, SupersedesID     int64
	Outcome, Evidence, SourceName, ObservedAt string
}

func concessionOutcome(outcome string) bool {
	return outcome == "conceded_home" || outcome == "conceded_away" || outcome == "possible_concession"
}
func validResultOutcome(outcome string) bool {
	switch outcome {
	case "played", "conceded_home", "conceded_away", "possible_concession", "abandoned", "postponed", "bye", "unknown":
		return true
	}
	return false
}

// CSV requires explicit local fixture IDs and documented normalised kinds.
// It deliberately does not guess Play-Cricket codes or a conceding side.
func ParseFixtureResultsCSV(raw []byte) ([]FixtureResultInput, error) {
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, fixtureValidation("CSV must be valid UTF-8 without null characters")
	}
	if len(raw) > 4<<20 {
		return nil, fixtureValidation("CSV exceeds 4MiB")
	}
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})))
	header, err := reader.Read()
	if err != nil {
		return nil, fixtureValidation("CSV header required")
	}
	if len(header) != 3 || strings.TrimSpace(header[0]) != "fixture_id" || strings.TrimSpace(header[1]) != "outcome" || strings.TrimSpace(header[2]) != "evidence" {
		return nil, fixtureValidation("CSV header must be fixture_id,outcome,evidence")
	}
	out := []FixtureResultInput{}
	seen := map[int64]bool{}
	for row := 2; ; row++ {
		cells, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fixtureValidation(fmt.Sprintf("invalid CSV row %d", row))
		}
		if len(out) >= 5000 {
			return nil, fixtureValidation("CSV exceeds 5,000 rows")
		}
		id, err := strconv.ParseInt(strings.TrimSpace(cells[0]), 10, 64)
		kind, evidence := strings.TrimSpace(cells[1]), strings.TrimSpace(cells[2])
		if err != nil || id <= 0 || seen[id] || !validResultOutcome(kind) || len(evidence) > 20000 || (concessionOutcome(kind) && evidence == "") {
			return nil, fixtureValidation(fmt.Sprintf("row %d: unique local fixture ID, known outcome and concession evidence required", row))
		}
		seen[id] = true
		out = append(out, FixtureResultInput{id, kind, evidence})
	}
	if len(out) == 0 {
		return nil, fixtureValidation("CSV has no results")
	}
	return out, nil
}
func resultFingerprint(source string, row FixtureResultInput) string {
	sum := sha256.Sum256([]byte(source + "\x00" + strconv.FormatInt(row.FixtureID, 10) + "\x00" + row.Outcome + "\x00" + row.Evidence))
	return hex.EncodeToString(sum[:])
}
func (s *Service) ImportFixtureResultsCSV(ctx context.Context, p Principal, competitionID int64, source string, raw []byte) (ResultImportSummary, error) {
	var summary ResultImportSummary
	source = strings.TrimSpace(source)
	if source == "" || len(source) > 200 || strings.ContainsAny(source, "\r\n\x00") {
		return summary, fixtureValidation("source name required (maximum 200 characters)")
	}
	inputs, err := ParseFixtureResultsCSV(raw)
	if err != nil {
		return summary, err
	}
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return summary, err
	}
	defer tx.Rollback(ctx)
	c, err := loadCompetition(ctx, tx, competitionID, true)
	if err != nil {
		return summary, err
	}
	if c.PublicationID == 0 {
		return summary, fixtureValidation("results require a published local fixture schedule")
	}
	sum := sha256.Sum256(raw)
	contentHash := hex.EncodeToString(sum[:])
	err = tx.QueryRow(ctx, `INSERT INTO junior_result_imports(competition_id,source_name,content_sha256,imported_by_admin_id) VALUES($1,$2,$3,$4) ON CONFLICT(competition_id,source_name,content_sha256) DO NOTHING RETURNING id`, competitionID, source, contentHash, p.AdminID).Scan(&summary.ImportID)
	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, `SELECT id FROM junior_result_imports WHERE competition_id=$1 AND source_name=$2 AND content_sha256=$3`, competitionID, source, contentHash).Scan(&summary.ImportID)
		summary.Replayed = true
		return summary, err
	}
	if err != nil {
		return summary, err
	}
	for _, input := range inputs {
		var mapped bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_publication_fixtures WHERE publication_id=$1 AND fixture_id=$2)`, c.PublicationID, input.FixtureID).Scan(&mapped)
		if err != nil {
			return summary, err
		}
		if !mapped {
			return summary, fixtureValidation(fmt.Sprintf("fixture %d is not in this competition's current official publication", input.FixtureID))
		}
		var previousID int64
		var previousFingerprint string
		err = tx.QueryRow(ctx, `SELECT id,fingerprint FROM junior_result_observations WHERE fixture_id=$1 ORDER BY id DESC LIMIT 1`, input.FixtureID).Scan(&previousID, &previousFingerprint)
		if err != nil && err != pgx.ErrNoRows {
			return summary, err
		}
		fingerprint := resultFingerprint(source, input)
		var observationID int64
		err = tx.QueryRow(ctx, `INSERT INTO junior_result_observations(import_id,fixture_id,publication_id,outcome,evidence,fingerprint,supersedes_id) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7::bigint,0)) RETURNING id`, summary.ImportID, input.FixtureID, c.PublicationID, input.Outcome, input.Evidence, fingerprint, previousID).Scan(&observationID)
		if err != nil {
			return summary, err
		}
		summary.Observations++
		if fingerprint == previousFingerprint {
			continue
		}
		var candidateID int64
		err = tx.QueryRow(ctx, `SELECT id FROM junior_concession_candidates WHERE fixture_id=$1 FOR UPDATE`, input.FixtureID).Scan(&candidateID)
		if err == pgx.ErrNoRows {
			if !concessionOutcome(input.Outcome) {
				continue
			}
			err = tx.QueryRow(ctx, `INSERT INTO junior_concession_candidates(fixture_id,current_observation_id,state) VALUES($1,$2,'awaiting_review') RETURNING id`, input.FixtureID, observationID).Scan(&candidateID)
			if err != nil {
				return summary, err
			}
			summary.Candidates++
		} else if err != nil {
			return summary, err
		} else {
			_, err = tx.Exec(ctx, `UPDATE junior_concession_candidates SET current_observation_id=$2,state='source_changed',revision=revision+1,updated_at=now() WHERE id=$1`, candidateID, observationID)
			if err != nil {
				return summary, err
			}
		}
	}
	if err = Audit(ctx, tx, p, 0, "competition", competitionID, "results_imported", summary); err != nil {
		return summary, err
	}
	return summary, tx.Commit(ctx)
}
func (s *Service) ListResultObservations(ctx context.Context, p Principal, competitionID int64) ([]ResultObservation, error) {
	if _, err := s.GetCompetition(ctx, p, competitionID); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT o.id,o.fixture_id,o.import_id,COALESCE(o.supersedes_id,0),o.outcome,o.evidence,i.source_name,o.observed_at::text FROM junior_result_observations o JOIN junior_result_imports i ON i.id=o.import_id JOIN junior_fixtures f ON f.id=o.fixture_id JOIN junior_teams h ON h.id=f.home_team_id JOIN junior_teams a ON a.id=f.away_team_id WHERE i.competition_id=$1 AND ($2::int=0 OR h.club_id=$2 OR a.club_id=$2) ORDER BY o.id DESC`, competitionID, p.ClubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResultObservation{}
	for rows.Next() {
		var o ResultObservation
		if err = rows.Scan(&o.ID, &o.FixtureID, &o.ImportID, &o.SupersedesID, &o.Outcome, &o.Evidence, &o.SourceName, &o.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
