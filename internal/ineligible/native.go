package ineligible

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const nativeFormOrigin = "native_form"

var nativeSubmissionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{20,128}$`)

// NativeEvidence records the immutable copy of a file supplied with the
// website form. StorageKey is deliberately excluded from the content digest:
// an HTTP retry can produce a second temporary copy of the same bytes and must
// still resolve to the original intake revision.
type NativeEvidence struct {
	OriginalName string `json:"original_name"`
	MediaType    string `json:"media_type"`
	ByteSize     int64  `json:"byte_size"`
	SHA256       string `json:"sha256"`
	StorageKey   string `json:"storage_key"`
}

// NativeSubmission is the website equivalent of the reviewed Google Form
// A:N contract. OffendingClub and Team must be populated from the selected
// active team in the database rather than trusted from browser text.
type NativeSubmission struct {
	SubmissionID       string
	SubmittedAt        time.Time
	ReporterEmail      string
	ReporterName       string
	ReporterRole       string
	ReporterPhone      string
	ReportingClub      string
	OffendingClub      string
	Team               string
	TeamID             int
	Player             string
	FixtureDate        time.Time
	Reason             string
	AdditionalInfo     string
	AdditionalEvidence string
	Score              string
	Evidence           *NativeEvidence
	Training           bool
}

// NativeStageResult identifies the private intake record. Disposition is new,
// changed, or unchanged and lets the HTTP layer remove a redundant temporary
// upload after an idempotent retry.
type NativeStageResult struct {
	IntakeID    int64
	Reference   string
	Disposition string
}

type preparedNativeSubmission struct {
	ExternalKey     string
	SourceReference string
	ExternalCreated time.Time
	ReportingClub   string
	OffendingClub   string
	Team            string
	Player          string
	FixtureDate     time.Time
	RawData         map[string]any
	RawSHA256       string
	HeaderSHA256    string
	Training        bool
}

// StageNative appends a native website report to the same intake/revision
// model used by Google polling. It intentionally creates no sanction case,
// access token, correspondence, or outbound email.
func (s *PGStore) StageNative(ctx context.Context, submission NativeSubmission) (NativeStageResult, error) {
	if s == nil || s.pool == nil {
		return NativeStageResult{}, fmt.Errorf("database pool is nil")
	}
	prepared, err := prepareNativeSubmission(submission)
	if err != nil {
		return NativeStageResult{}, err
	}
	rawJSON, err := json.Marshal(prepared.RawData)
	if err != nil {
		return NativeStageResult{}, fmt.Errorf("encode native intake: %w", err)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return NativeStageResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "gmcl_native_ineligible|"+prepared.ExternalKey); err != nil {
		return NativeStageResult{}, err
	}

	var runID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO sanction_intake_sync_runs(
			origin,status,source_reference,header_sha256,rows_seen,
			triggered_by_type
		) VALUES('native_form','running',$1,$2,1,'public') RETURNING id
	`, prepared.SourceReference, prepared.HeaderSHA256).Scan(&runID)
	if err != nil {
		return NativeStageResult{}, err
	}

	var intakeID int64
	var latestRevision int
	var latestSHA, currentState string
	var existingTraining bool
	err = tx.QueryRow(ctx, `
		SELECT i.id,i.latest_revision,i.state,i.is_training,
		       COALESCE((SELECT r.raw_sha256 FROM sanction_intake_revisions r
		                 WHERE r.intake_id=i.id ORDER BY r.revision DESC LIMIT 1),'')
		FROM sanction_intakes i
		WHERE i.origin='native_form' AND i.external_key=$1
		FOR UPDATE
	`, prepared.ExternalKey).Scan(&intakeID, &latestRevision, &currentState, &existingTraining, &latestSHA)

	disposition := "unchanged"
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = tx.QueryRow(ctx, `
			INSERT INTO sanction_intakes(
				origin,external_key,source_reference,external_created_at,state,
				reporting_club_text,offending_club_text,team_text,player_text,
				fixture_date,latest_revision,is_training
			) VALUES('native_form',$1,$2,$3,'new',$4,$5,$6,$7,$8,1,$9)
			RETURNING id
		`, prepared.ExternalKey, prepared.SourceReference, prepared.ExternalCreated,
			prepared.ReportingClub, prepared.OffendingClub, prepared.Team,
			prepared.Player, prepared.FixtureDate, prepared.Training).Scan(&intakeID)
		if err == nil {
			_, err = tx.Exec(ctx, `
				INSERT INTO sanction_intake_revisions(
					intake_id,sync_run_id,revision,raw_data,raw_sha256,header_sha256
				) VALUES($1,$2,1,$3::jsonb,$4,$5)
			`, intakeID, runID, string(rawJSON), prepared.RawSHA256, prepared.HeaderSHA256)
		}
		disposition = "new"
	case err != nil:
		return NativeStageResult{}, err
	case existingTraining != prepared.Training:
		return NativeStageResult{}, fmt.Errorf("native submission training classification cannot change")
	case latestSHA != prepared.RawSHA256:
		nextRevision := latestRevision + 1
		resolvedChange := currentState == "linked" || currentState == "duplicate" || currentState == "ignored"
		resolvedChangeMessage := ""
		if resolvedChange {
			resolvedChangeMessage = fmt.Sprintf("source changed after prior triage resolution; review immutable revision %d before proceeding", nextRevision)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO sanction_intake_revisions(
				intake_id,sync_run_id,revision,raw_data,raw_sha256,header_sha256
			) VALUES($1,$2,$3,$4::jsonb,$5,$6)
		`, intakeID, runID, nextRevision, string(rawJSON), prepared.RawSHA256, prepared.HeaderSHA256)
		if err == nil {
			_, err = tx.Exec(ctx, `
				UPDATE sanction_intakes SET
					source_reference=$2,external_created_at=$3,
					state=CASE WHEN state IN ('linked','duplicate','ignored') THEN 'exception' ELSE 'new' END,
					reporting_club_text=$4,offending_club_text=$5,team_text=$6,
					player_text=$7,fixture_date=$8,latest_revision=$9,
					exception_message=CASE WHEN state IN ('linked','duplicate','ignored') THEN $10 ELSE NULL END,
					updated_at=now()
				WHERE id=$1
			`, intakeID, prepared.SourceReference, prepared.ExternalCreated,
				prepared.ReportingClub, prepared.OffendingClub, prepared.Team,
				prepared.Player, prepared.FixtureDate, nextRevision, resolvedChangeMessage)
		}
		if err == nil && resolvedChange {
			_, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,metadata)
				SELECT DISTINCT link.case_id,'linked_intake_revision_changed','system','Native ineligible-player intake',$2,
					jsonb_build_object('intake_id',$1::bigint,'intake_revision',$3::integer,'origin','native_form')
				FROM sanction_intake_effective_case_links link WHERE link.intake_id=$1`, intakeID, resolvedChangeMessage, nextRevision)
		}
		if err == nil && resolvedChange {
			err = invalidateLinkedCaseResponseWindows(ctx, tx, intakeID, resolvedChangeMessage, nextRevision)
		}
		disposition = "changed"
	default:
		_ = currentState
	}
	if err != nil {
		return NativeStageResult{}, err
	}

	rowsNew, rowsChanged := 0, 0
	if disposition == "new" {
		rowsNew = 1
	} else if disposition == "changed" {
		rowsChanged = 1
	}
	_, err = tx.Exec(ctx, `
		UPDATE sanction_intake_sync_runs SET
			status='succeeded',rows_new=$2,rows_changed=$3,completed_at=now()
		WHERE id=$1 AND status='running'
	`, runID, rowsNew, rowsChanged)
	if err != nil {
		return NativeStageResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NativeStageResult{}, err
	}
	return NativeStageResult{
		IntakeID: intakeID, Reference: fmt.Sprintf("IPR-%06d", intakeID), Disposition: disposition,
	}, nil
}

func prepareNativeSubmission(submission NativeSubmission) (preparedNativeSubmission, error) {
	submission.SubmissionID = strings.TrimSpace(submission.SubmissionID)
	submission.ReporterEmail = strings.ToLower(strings.TrimSpace(submission.ReporterEmail))
	submission.ReporterName = strings.TrimSpace(submission.ReporterName)
	submission.ReporterRole = strings.TrimSpace(submission.ReporterRole)
	submission.ReporterPhone = strings.TrimSpace(submission.ReporterPhone)
	submission.ReportingClub = strings.TrimSpace(submission.ReportingClub)
	submission.OffendingClub = strings.TrimSpace(submission.OffendingClub)
	submission.Team = strings.TrimSpace(submission.Team)
	submission.Player = strings.TrimSpace(submission.Player)
	submission.Reason = strings.TrimSpace(submission.Reason)
	if !nativeSubmissionIDPattern.MatchString(submission.SubmissionID) {
		return preparedNativeSubmission{}, fmt.Errorf("invalid native submission id")
	}
	if submission.SubmittedAt.IsZero() {
		return preparedNativeSubmission{}, fmt.Errorf("submission timestamp is required")
	}
	if submission.FixtureDate.IsZero() {
		return preparedNativeSubmission{}, fmt.Errorf("fixture date is required")
	}
	for label, value := range map[string]string{
		"reporter email": submission.ReporterEmail,
		"reporter name":  submission.ReporterName,
		"reporter role":  submission.ReporterRole,
		"reporter phone": submission.ReporterPhone,
		"reporting club": submission.ReportingClub,
		"offending club": submission.OffendingClub,
		"team":           submission.Team,
		"player":         submission.Player,
		"reason":         submission.Reason,
	} {
		if value == "" {
			return preparedNativeSubmission{}, fmt.Errorf("%s is required", label)
		}
	}
	if !strings.Contains(submission.ReporterEmail, "@") {
		return preparedNativeSubmission{}, fmt.Errorf("reporter email is invalid")
	}
	if submission.TeamID <= 0 {
		return preparedNativeSubmission{}, fmt.Errorf("offending team id is required")
	}

	schema := DefaultGoogleFormSchema()
	fileValue := any("")
	if submission.Evidence != nil {
		fileValue = []NativeEvidence{*submission.Evidence}
	}
	nameAndRole := submission.ReporterName + " — " + submission.ReporterRole
	raw := map[string]any{
		"Timestamp":      submission.SubmittedAt.UTC().Format(time.RFC3339Nano),
		"Email address":  submission.ReporterEmail,
		"reporter name":  submission.ReporterName,
		"reporter role":  submission.ReporterRole,
		"reporter phone": submission.ReporterPhone,
		"Name of defaulting player as shown on scorecard": submission.Player,
		"Reason you believe the player is ineligible":     submission.Reason,
		"Additional Info":                 strings.TrimSpace(submission.AdditionalInfo),
		"Your Club":                       submission.ReportingClub,
		"Your Name & Role at Club/League": nameAndRole,
		"Your Preferred tel no":           submission.ReporterPhone,
		"Offending Club's Name":           submission.OffendingClub,
		"Team in question":                submission.Team,
		"Fixture Date":                    submission.FixtureDate.Format("2006-01-02"),
		"Additional Evidence":             strings.TrimSpace(submission.AdditionalEvidence),
		"File Upload":                     fileValue,
		"Score":                           strings.TrimSpace(submission.Score),
		"_native_submission_id":           submission.SubmissionID,
		"_native_team_id":                 submission.TeamID,
		"_training_case":                  submission.Training,
	}

	// The content checksum omits only the random storage key, retaining the
	// filename, media type, size, and file SHA-256 for retry idempotency.
	digestRaw := make(map[string]any, len(raw))
	for key, value := range raw {
		digestRaw[key] = value
	}
	if submission.Evidence != nil {
		digestRaw["File Upload"] = []map[string]any{{
			"original_name": submission.Evidence.OriginalName,
			"media_type":    submission.Evidence.MediaType,
			"byte_size":     submission.Evidence.ByteSize,
			"sha256":        submission.Evidence.SHA256,
		}}
	}
	canonical, _ := json.Marshal(digestRaw)
	keyDigest := sha256.Sum256([]byte("native-form-v1|" + submission.SubmissionID))
	contentDigest := sha256.Sum256(canonical)

	return preparedNativeSubmission{
		ExternalKey:     hex.EncodeToString(keyDigest[:]),
		SourceReference: "native-form://sanctions/report/" + submission.SubmissionID,
		ExternalCreated: submission.SubmittedAt.UTC(),
		ReportingClub:   submission.ReportingClub,
		OffendingClub:   submission.OffendingClub,
		Team:            submission.Team,
		Player:          submission.Player,
		FixtureDate:     submission.FixtureDate,
		RawData:         raw,
		RawSHA256:       hex.EncodeToString(contentDigest[:]),
		HeaderSHA256:    headerSHA256(schema.Headers),
		Training:        submission.Training,
	}, nil
}
