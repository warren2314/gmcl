package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ineligibledomain "cricket-ground-feedback/internal/ineligible"

	"github.com/jackc/pgx/v5"
)

type ineligibleIntakeMerge struct {
	CaseID            int64
	IntakeID          int64
	RevisionID        int64
	TeamID            int64
	PlayerName        string
	PlayCricketPlayer *int64
	PlayCricketMatch  *int64
	OffendingClubName string
	ReportingClubID   *int32
	ReportingClubName string
	LeagueOrigin      bool
	CreatedByAdminID  int32
	Primary           bool
	Relationship      string
}

// mergeIneligibleIntakeIntoCase only adds append-only provenance and private
// evidence. It never creates correspondence, effects, or an outbox item.
func mergeIneligibleIntakeIntoCase(ctx context.Context, tx pgx.Tx, input ineligibleIntakeMerge) error {
	player := strings.TrimSpace(input.PlayerName)
	if input.Relationship == "" {
		input.Relationship = "supporting"
	}
	if input.CaseID <= 0 || input.IntakeID <= 0 || input.RevisionID <= 0 || input.TeamID <= 0 || player == "" {
		return fmt.Errorf("incomplete intake case merge")
	}

	teamSubjectID, err := upsertIneligibleTeamSubject(ctx, tx, input)
	if err != nil {
		return err
	}
	if err = linkIneligibleSubjectRevision(ctx, tx, teamSubjectID, input); err != nil {
		return err
	}
	playerSubjectID, err := upsertIneligiblePlayerSubject(ctx, tx, input)
	if err != nil {
		return err
	}
	if err = linkIneligibleSubjectRevision(ctx, tx, playerSubjectID, input); err != nil {
		return err
	}
	var matchSubjectID *int64
	if input.PlayCricketMatch != nil {
		var resolvedMatchSubjectID int64
		_, err = tx.Exec(ctx, `
			INSERT INTO sanction_case_subjects(case_id,subject_type,play_cricket_match_id,is_primary,metadata)
			VALUES($1,'match',$2,$5,jsonb_build_object('intake_id',$3::bigint,'intake_revision_id',$4::bigint))
			ON CONFLICT (case_id,play_cricket_match_id) WHERE subject_type='match' AND play_cricket_match_id IS NOT NULL
			DO NOTHING
		`, input.CaseID, *input.PlayCricketMatch, input.IntakeID, input.RevisionID, input.Primary)
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT id FROM sanction_case_subjects WHERE case_id=$1 AND subject_type='match' AND play_cricket_match_id=$2`, input.CaseID, *input.PlayCricketMatch).Scan(&resolvedMatchSubjectID)
		}
		if err != nil {
			return fmt.Errorf("merge intake match subject: %w", err)
		}
		if err = linkIneligibleSubjectRevision(ctx, tx, resolvedMatchSubjectID, input); err != nil {
			return err
		}
		matchSubjectID = &resolvedMatchSubjectID
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO sanction_case_parties(case_id,party_type,name,team_id,relationship)
		VALUES($1,'club',$2,$3,'offending_club')
		ON CONFLICT (case_id,team_id) WHERE relationship='offending_club' AND team_id IS NOT NULL DO NOTHING
	`, input.CaseID, input.OffendingClubName, input.TeamID)
	if err != nil {
		return fmt.Errorf("merge offending party: %w", err)
	}
	var reportingPartyID *int64
	if input.LeagueOrigin {
		_, err = tx.Exec(ctx, `
			INSERT INTO sanction_case_parties(case_id,party_type,name,relationship)
			VALUES($1,'league','GMCL Official','league')
			ON CONFLICT (case_id,relationship) WHERE relationship='league' AND party_type='league' DO NOTHING
		`, input.CaseID)
		if err != nil {
			return fmt.Errorf("merge league-origin party: %w", err)
		}
		if input.Primary {
			if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET reporting_club_id=NULL,updated_at=now() WHERE id=$1`, input.CaseID); err != nil {
				return fmt.Errorf("project league-origin report: %w", err)
			}
		}
	} else {
		if input.ReportingClubID == nil || strings.TrimSpace(input.ReportingClubName) == "" {
			return fmt.Errorf("external reporting club is not mapped")
		}
		var partyID int64
		_, err = tx.Exec(ctx, `
			INSERT INTO sanction_case_parties(case_id,party_type,name,club_id,relationship)
			VALUES($1,'club',$2,$3,'reporting_club')
			ON CONFLICT (case_id,club_id) WHERE relationship='reporting_club' AND club_id IS NOT NULL
			DO NOTHING
		`, input.CaseID, input.ReportingClubName, *input.ReportingClubID)
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT id FROM sanction_case_parties WHERE case_id=$1 AND club_id=$2 AND relationship='reporting_club'`, input.CaseID, *input.ReportingClubID).Scan(&partyID)
		}
		if err != nil {
			return fmt.Errorf("merge reporting party: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO sanction_case_reporting_club_intakes(case_id,club_id,intake_id,party_id,created_by_admin_id)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING
		`, input.CaseID, *input.ReportingClubID, input.IntakeID, partyID, input.CreatedByAdminID)
		if err != nil {
			return fmt.Errorf("retain reporting club provenance: %w", err)
		}
		reportingPartyID = &partyID
		// Preserve the original primary reporting club, but populate legacy cases
		// whose scalar projection predates reporting-club mapping.
		_, err = tx.Exec(ctx, `UPDATE sanction_cases SET reporting_club_id=CASE WHEN $3::boolean THEN $2 ELSE COALESCE(reporting_club_id,$2) END,updated_at=now() WHERE id=$1`, input.CaseID, *input.ReportingClubID, input.Primary)
		if err != nil {
			return fmt.Errorf("project primary reporting club: %w", err)
		}
	}

	_, err = tx.Exec(ctx, `INSERT INTO sanction_case_intake_merge_resolutions(
		case_id,intake_id,revision_id,relationship,team_id,team_subject_id,player_subject_id,match_subject_id,
		reporting_club_id,reporting_party_id,league_origin,created_by_admin_id
	)
	SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12
	WHERE NOT EXISTS (
		SELECT 1 FROM sanction_case_intake_merge_resolutions prior
		WHERE prior.id=(SELECT latest.id FROM sanction_case_intake_merge_resolutions latest
			WHERE latest.case_id=$1 AND latest.intake_id=$2 ORDER BY latest.id DESC LIMIT 1)
		  AND prior.revision_id=$3 AND prior.relationship=$4 AND prior.team_id=$5
		  AND prior.team_subject_id=$6 AND prior.player_subject_id=$7
		  AND prior.match_subject_id IS NOT DISTINCT FROM $8::bigint
		  AND prior.reporting_club_id IS NOT DISTINCT FROM $9::integer
		  AND prior.reporting_party_id IS NOT DISTINCT FROM $10::bigint
		  AND prior.league_origin=$11
	)`, input.CaseID, input.IntakeID, input.RevisionID, input.Relationship, input.TeamID, teamSubjectID, playerSubjectID,
		matchSubjectID, input.ReportingClubID, reportingPartyID, input.LeagueOrigin, input.CreatedByAdminID)
	if err != nil {
		return fmt.Errorf("record effective intake merge resolution: %w", err)
	}

	if err = attachIneligibleIntakeEvidence(ctx, tx, input.CaseID, input.IntakeID, input.CreatedByAdminID); err != nil {
		return err
	}
	return nil
}

func upsertIneligibleTeamSubject(ctx context.Context, tx pgx.Tx, input ineligibleIntakeMerge) (int64, error) {
	var id int64
	_, err := tx.Exec(ctx, `
		INSERT INTO sanction_case_subjects(case_id,subject_type,team_id,is_primary,metadata)
		VALUES($1,'team',$2,$5,jsonb_build_object('intake_id',$3::bigint,'intake_revision_id',$4::bigint))
		ON CONFLICT (case_id,team_id) WHERE subject_type='team'
		DO NOTHING
	`, input.CaseID, input.TeamID, input.IntakeID, input.RevisionID, input.Primary)
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT id FROM sanction_case_subjects WHERE case_id=$1 AND subject_type='team' AND team_id=$2`, input.CaseID, input.TeamID).Scan(&id)
	}
	if err != nil {
		return 0, fmt.Errorf("merge intake team subject: %w", err)
	}
	return id, nil
}

func upsertIneligiblePlayerSubject(ctx context.Context, tx pgx.Tx, input ineligibleIntakeMerge) (int64, error) {
	var id int64
	var err error
	if input.PlayCricketPlayer != nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO sanction_case_subjects(case_id,subject_type,team_id,player_name,play_cricket_player_id,is_primary,metadata)
			VALUES($1,'player',$2,$3,$4,$7,jsonb_build_object('intake_id',$5::bigint,'intake_revision_id',$6::bigint))
			ON CONFLICT (case_id,play_cricket_player_id) WHERE subject_type='player' AND play_cricket_player_id IS NOT NULL
			DO NOTHING
		`, input.CaseID, input.TeamID, strings.TrimSpace(input.PlayerName), *input.PlayCricketPlayer, input.IntakeID, input.RevisionID, input.Primary)
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT id FROM sanction_case_subjects WHERE case_id=$1 AND subject_type='player' AND play_cricket_player_id=$2`, input.CaseID, *input.PlayCricketPlayer).Scan(&id)
		}
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO sanction_case_subjects(case_id,subject_type,team_id,player_name,is_primary,metadata)
			VALUES($1,'player',$2,$3,$6,jsonb_build_object('intake_id',$4::bigint,'intake_revision_id',$5::bigint))
			ON CONFLICT (case_id,team_id,(LOWER(BTRIM(player_name)))) WHERE subject_type='player' AND play_cricket_player_id IS NULL
			DO NOTHING
		`, input.CaseID, input.TeamID, strings.TrimSpace(input.PlayerName), input.IntakeID, input.RevisionID, input.Primary)
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT id FROM sanction_case_subjects WHERE case_id=$1 AND subject_type='player' AND team_id=$2 AND play_cricket_player_id IS NULL AND LOWER(BTRIM(player_name))=LOWER(BTRIM($3))`, input.CaseID, input.TeamID, strings.TrimSpace(input.PlayerName)).Scan(&id)
		}
	}
	if err != nil {
		return 0, fmt.Errorf("merge intake player subject: %w", err)
	}
	return id, nil
}

func linkIneligibleSubjectRevision(ctx context.Context, tx pgx.Tx, subjectID int64, input ineligibleIntakeMerge) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO sanction_case_subject_intakes(subject_id,case_id,intake_id,revision_id,created_by_admin_id)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING
	`, subjectID, input.CaseID, input.IntakeID, input.RevisionID, input.CreatedByAdminID)
	if err != nil {
		return fmt.Errorf("retain subject intake provenance: %w", err)
	}
	return nil
}

func projectIneligibleIntakeMergeState(ctx context.Context, tx pgx.Tx, intakeID int64) error {
	_, err := tx.Exec(ctx, `UPDATE sanction_intakes intake
		SET state=CASE WHEN EXISTS(
			SELECT 1 FROM sanction_intake_effective_case_links link
			JOIN sanction_intake_revisions latest
			  ON latest.intake_id=intake.id AND latest.revision=intake.latest_revision
			WHERE link.intake_id=intake.id AND link.relationship<>'duplicate'
			  AND NOT EXISTS(
				SELECT 1 FROM sanction_case_intake_merge_resolutions resolution
				WHERE resolution.id=(SELECT current.id FROM sanction_case_intake_merge_resolutions current
					WHERE current.case_id=link.case_id AND current.intake_id=intake.id
					  AND current.relationship=link.relationship ORDER BY current.id DESC LIMIT 1)
				  AND resolution.revision_id=latest.id
			  )
		) THEN 'exception' ELSE 'linked' END,
		exception_message=CASE WHEN EXISTS(
			SELECT 1 FROM sanction_intake_effective_case_links link
			JOIN sanction_intake_revisions latest
			  ON latest.intake_id=intake.id AND latest.revision=intake.latest_revision
			WHERE link.intake_id=intake.id AND link.relationship<>'duplicate'
			  AND NOT EXISTS(
				SELECT 1 FROM sanction_case_intake_merge_resolutions resolution
				WHERE resolution.id=(SELECT current.id FROM sanction_case_intake_merge_resolutions current
					WHERE current.case_id=link.case_id AND current.intake_id=intake.id
					  AND current.relationship=link.relationship ORDER BY current.id DESC LIMIT 1)
				  AND resolution.revision_id=latest.id
			  )
		) THEN COALESCE(NULLIF(intake.exception_message,''),'latest source revision still requires review in another linked case') ELSE NULL END,
		updated_at=now()
		WHERE intake.id=$1`, intakeID)
	if err != nil {
		return fmt.Errorf("project intake merge state: %w", err)
	}
	return nil
}

type retainedIntakeEvidence struct {
	RevisionID int64
	Kind       string
	SourceKey  string
	Name       string
	MediaType  string
	Size       int64
	SHA256     string
	StorageKey string
}

func attachIneligibleIntakeEvidence(ctx context.Context, tx pgx.Tx, caseID, intakeID int64, adminID int32) error {
	items, err := loadRetainedIneligibleEvidence(ctx, tx, intakeID)
	if err != nil {
		return err
	}
	for _, item := range items {
		var already bool
		err = tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM sanction_case_intake_evidence
			 WHERE case_id=$1 AND intake_id=$2 AND revision_id=$3 AND source_kind=$4 AND source_key=$5 AND source_sha256=$6)
		`, caseID, intakeID, item.RevisionID, item.Kind, item.SourceKey, item.SHA256).Scan(&already)
		if err != nil {
			return fmt.Errorf("inspect intake evidence bridge: %w", err)
		}
		if already {
			continue
		}
		storageKey, err := verifyAndRetainCaseEvidence(item)
		if err != nil {
			return err
		}
		var evidenceID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO sanction_case_evidence(case_id,visibility,original_name,media_type,byte_size,storage_key,sha256,uploaded_by_type,uploaded_by_id)
			VALUES($1,'private',$2,$3,$4,$5,$6,'import',$7) RETURNING id
		`, caseID, retainedEvidenceName(item.Name), item.MediaType, item.Size, storageKey, item.SHA256, adminID).Scan(&evidenceID)
		if err != nil {
			return fmt.Errorf("attach private intake evidence: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO sanction_case_intake_evidence(case_id,evidence_id,intake_id,revision_id,source_kind,source_key,source_storage_key,source_sha256,created_by_admin_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, caseID, evidenceID, intakeID, item.RevisionID, item.Kind, item.SourceKey, item.StorageKey, item.SHA256, adminID)
		if err != nil {
			return fmt.Errorf("record intake evidence provenance: %w", err)
		}
	}
	return nil
}

func loadRetainedIneligibleEvidence(ctx context.Context, tx pgx.Tx, intakeID int64) ([]retainedIntakeEvidence, error) {
	items := []retainedIntakeEvidence{}
	rows, err := tx.Query(ctx, `
		SELECT a.revision_id,a.google_drive_file_id,a.original_filename,a.content_type,a.size_bytes,a.sha256,a.storage_key
		FROM sanction_intake_attachments a WHERE a.intake_id=$1 ORDER BY a.revision_id,a.id
	`, intakeID)
	if err != nil {
		return nil, fmt.Errorf("load retained Google evidence: %w", err)
	}
	for rows.Next() {
		var item retainedIntakeEvidence
		item.Kind = "google_drive"
		if err = rows.Scan(&item.RevisionID, &item.SourceKey, &item.Name, &item.MediaType, &item.Size, &item.SHA256, &item.StorageKey); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	revisions, err := tx.Query(ctx, `
		SELECT r.id,r.raw_data FROM sanction_intake_revisions r JOIN sanction_intakes i ON i.id=r.intake_id
		WHERE r.intake_id=$1 AND i.origin='native_form' ORDER BY r.revision
	`, intakeID)
	if err != nil {
		return nil, fmt.Errorf("load retained native evidence: %w", err)
	}
	defer revisions.Close()
	for revisions.Next() {
		var revisionID int64
		var raw []byte
		if err = revisions.Scan(&revisionID, &raw); err != nil {
			return nil, err
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil {
			return nil, fmt.Errorf("native intake revision %d has malformed evidence metadata", revisionID)
		}
		var files []ineligibledomain.NativeEvidence
		if value := envelope["File Upload"]; len(value) > 0 && string(value) != `""` {
			if err = json.Unmarshal(value, &files); err != nil {
				return nil, fmt.Errorf("native intake revision %d has malformed evidence metadata: %w", revisionID, err)
			}
		}
		for _, file := range files {
			items = append(items, retainedIntakeEvidence{RevisionID: revisionID, Kind: "native_upload", SourceKey: file.SHA256, Name: file.OriginalName, MediaType: file.MediaType, Size: file.ByteSize, SHA256: strings.ToLower(file.SHA256), StorageKey: file.StorageKey})
		}
	}
	return items, revisions.Err()
}

func verifyAndRetainCaseEvidence(item retainedIntakeEvidence) (string, error) {
	if item.RevisionID <= 0 || strings.TrimSpace(item.SourceKey) == "" || filepath.Base(item.Name) == "." || strings.TrimSpace(item.MediaType) == "" || item.Size <= 0 || len(item.SHA256) != 64 {
		return "", fmt.Errorf("retained intake evidence provenance is incomplete")
	}
	var sourcePath string
	var err error
	switch item.Kind {
	case "native_upload":
		if item.StorageKey == "" || filepath.Base(item.StorageKey) != item.StorageKey {
			return "", fmt.Errorf("native intake evidence storage key is invalid")
		}
		sourcePath, err = retainedEvidencePath(evidenceDir(), item.StorageKey)
		if err != nil {
			return "", err
		}
	case "google_drive":
		base := strings.TrimSpace(os.Getenv("INELIGIBLE_UPLOAD_DIR"))
		if base == "" {
			base = "/app/data/ineligible-uploads"
		}
		relative := filepath.Clean(filepath.FromSlash(item.StorageKey))
		if filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("Google intake evidence storage key is invalid")
		}
		sourcePath, err = retainedEvidencePath(base, relative)
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unknown retained intake evidence source")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", fmt.Errorf("retained intake evidence is unavailable: %w", err)
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	if int64(len(data)) != item.Size || !strings.EqualFold(actual, item.SHA256) {
		return "", fmt.Errorf("retained intake evidence failed checksum verification")
	}
	if item.Kind == "native_upload" {
		return item.StorageKey, nil
	}
	if err = os.MkdirAll(evidenceDir(), 0700); err != nil {
		return "", err
	}
	key := "intake-" + strings.ToLower(item.SHA256)
	destination := filepath.Join(evidenceDir(), key)
	if destinationInfo, statErr := os.Lstat(destination); statErr == nil {
		if destinationInfo.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("case evidence destination is a symlink")
		}
		existing, readErr := os.ReadFile(destination)
		if readErr != nil {
			return "", readErr
		}
		existingSHA := fmt.Sprintf("%x", sha256.Sum256(existing))
		if int64(len(existing)) != item.Size || existingSHA != strings.ToLower(item.SHA256) {
			return "", fmt.Errorf("case evidence content-address collision")
		}
		return key, nil
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		destinationInfo, statErr := os.Lstat(destination)
		if statErr != nil {
			return "", statErr
		}
		if destinationInfo.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("case evidence destination is a symlink")
		}
		existing, readErr := os.ReadFile(destination)
		if readErr != nil {
			return "", readErr
		}
		existingSHA := fmt.Sprintf("%x", sha256.Sum256(existing))
		if int64(len(existing)) != item.Size || existingSHA != strings.ToLower(item.SHA256) {
			return "", fmt.Errorf("case evidence content-address collision")
		}
		return key, nil
	}
	if err != nil {
		return "", fmt.Errorf("retain case evidence copy: %w", err)
	}
	if _, err = output.Write(data); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return "", fmt.Errorf("retain case evidence copy: %w", err)
	}
	if err = output.Sync(); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return "", fmt.Errorf("retain case evidence copy: %w", err)
	}
	if err = output.Close(); err != nil {
		_ = os.Remove(destination)
		return "", fmt.Errorf("retain case evidence copy: %w", err)
	}
	return key, nil
}

func retainedEvidenceName(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	name := filepath.Base(value)
	if name == "." || name == "/" || name == "" {
		return "intake-evidence"
	}
	return name
}

func retainedEvidencePath(baseDirectory string, relativePath string) (string, error) {
	baseDirectory = strings.TrimSpace(baseDirectory)
	clean := filepath.Clean(relativePath)
	if baseDirectory == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("retained intake evidence path is invalid")
	}
	baseAbs, err := filepath.Abs(baseDirectory)
	if err != nil {
		return "", fmt.Errorf("resolve retained evidence directory: %w", err)
	}
	candidateAbs, err := filepath.Abs(filepath.Join(baseAbs, clean))
	if err != nil {
		return "", fmt.Errorf("resolve retained evidence path: %w", err)
	}
	relativeToBase, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil || relativeToBase == ".." || strings.HasPrefix(relativeToBase, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("retained intake evidence path escapes its storage directory")
	}
	current := baseAbs
	for _, component := range strings.Split(relativeToBase, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return "", fmt.Errorf("inspect retained evidence path: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("retained intake evidence path contains a symlink")
		}
	}
	return candidateAbs, nil
}
