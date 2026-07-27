package portal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const currentAuditHashVersion int16 = 2

type auditCanonicalRecord struct {
	HashVersion   int16          `json:"hash_version,omitempty"`
	ID            string         `json:"id"`
	ClubID        *int32         `json:"club_id,omitempty"`
	ActorUserID   string         `json:"actor_user_id,omitempty"`
	ActorKind     string         `json:"actor_kind"`
	LegacyAdminID *int32         `json:"legacy_admin_id,omitempty"`
	ActingRoleKey string         `json:"acting_role_key,omitempty"`
	Action        string         `json:"action"`
	TargetType    string         `json:"target_type"`
	TargetID      string         `json:"target_id,omitempty"`
	Outcome       string         `json:"outcome"`
	CorrelationID string         `json:"correlation_id"`
	Metadata      map[string]any `json:"metadata"`
	Position      int64          `json:"position"`
	PreviousHash  string         `json:"previous_hash,omitempty"`
	IPAddress     string         `json:"ip_address,omitempty"`
	UserAgent     string         `json:"user_agent,omitempty"`
	OccurredAt    string         `json:"occurred_at"`
}

func normalizeAuditTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func canonicalAuditBytes(
	eventID uuid.UUID,
	event AuditEvent,
	position int64,
	previousHash []byte,
	hashVersion int16,
) ([]byte, error) {
	if hashVersion != 1 && hashVersion != currentAuditHashVersion {
		return nil, fmt.Errorf("unsupported portal audit hash version %d", hashVersion)
	}
	versionField := int16(0)
	if hashVersion >= currentAuditHashVersion {
		versionField = hashVersion
	}
	metadata := event.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return json.Marshal(auditCanonicalRecord{
		HashVersion:   versionField,
		ID:            eventID.String(),
		ClubID:        event.ClubID,
		ActorUserID:   uuidString(event.ActorUserID),
		ActorKind:     event.ActorKind,
		LegacyAdminID: event.LegacyAdminID,
		ActingRoleKey: event.ActingRoleKey,
		Action:        event.Action,
		TargetType:    event.TargetType,
		TargetID:      event.TargetID,
		Outcome:       event.Outcome,
		CorrelationID: event.CorrelationID,
		Metadata:      metadata,
		Position:      position,
		PreviousHash:  hex.EncodeToString(previousHash),
		IPAddress:     event.IPAddress,
		UserAgent:     event.UserAgent,
		OccurredAt:    event.OccurredAt.UTC().Format(time.RFC3339Nano),
	})
}

type AuditChainHead struct {
	ClubID   *int32 `json:"club_id,omitempty"`
	Position int64  `json:"position"`
	Hash     string `json:"hash"`
}

type AuditIntegrityReport struct {
	VerifiedAt          time.Time        `json:"verified_at"`
	EventsChecked       int64            `json:"events_checked"`
	ChainsChecked       int              `json:"chains_checked"`
	LegacyHashEvents    int64            `json:"legacy_hash_events"`
	FullyVerifiedEvents int64            `json:"fully_verified_events"`
	Heads               []AuditChainHead `json:"heads"`
}

type AuditIntegrityError struct {
	EventID uuid.UUID
	Reason  string
}

func (err *AuditIntegrityError) Error() string {
	if err.EventID == uuid.Nil {
		return "portal audit integrity failure: " + err.Reason
	}
	return fmt.Sprintf(
		"portal audit integrity failure at event %s: %s",
		err.EventID,
		err.Reason,
	)
}

type auditStoredRecord struct {
	ID            uuid.UUID
	Event         AuditEvent
	Position      int64
	PositionValid bool
	PreviousHash  []byte
	EventHash     []byte
	HashVersion   int16
	ChainKey      string
	ChainClubID   *int32
}

type auditVerificationState struct {
	chainKey         string
	chainClubID      *int32
	expectedPosition int64
	expectedPrevious []byte
	started          bool
}

func verifyAuditRecord(
	state *auditVerificationState,
	record auditStoredRecord,
	report *AuditIntegrityReport,
) error {
	if !record.PositionValid || record.Position <= 0 {
		return &AuditIntegrityError{EventID: record.ID, Reason: "chain position is missing or invalid"}
	}
	if len(record.EventHash) != sha256.Size {
		return &AuditIntegrityError{EventID: record.ID, Reason: "event hash is not 32 bytes"}
	}
	if !state.started || state.chainKey != record.ChainKey {
		if state.started {
			report.Heads = append(report.Heads, AuditChainHead{
				ClubID:   state.chainClubID,
				Position: state.expectedPosition - 1,
				Hash:     hex.EncodeToString(state.expectedPrevious),
			})
		}
		state.started = true
		state.chainKey = record.ChainKey
		state.chainClubID = record.ChainClubID
		state.expectedPosition = 1
		state.expectedPrevious = nil
		report.ChainsChecked++
	}
	if record.Position != state.expectedPosition {
		return &AuditIntegrityError{
			EventID: record.ID,
			Reason: fmt.Sprintf(
				"expected chain position %d, found %d",
				state.expectedPosition,
				record.Position,
			),
		}
	}
	if !bytes.Equal(record.PreviousHash, state.expectedPrevious) {
		return &AuditIntegrityError{EventID: record.ID, Reason: "previous hash does not match the preceding event"}
	}
	switch record.HashVersion {
	case 1:
		report.LegacyHashEvents++
	case currentAuditHashVersion:
		canonical, err := canonicalAuditBytes(
			record.ID,
			record.Event,
			record.Position,
			record.PreviousHash,
			record.HashVersion,
		)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(canonical)
		if !bytes.Equal(record.EventHash, digest[:]) {
			return &AuditIntegrityError{EventID: record.ID, Reason: "event hash does not match canonical event data"}
		}
		report.FullyVerifiedEvents++
	default:
		return &AuditIntegrityError{
			EventID: record.ID,
			Reason:  fmt.Sprintf("unsupported hash version %d", record.HashVersion),
		}
	}
	report.EventsChecked++
	state.expectedPosition++
	state.expectedPrevious = append(state.expectedPrevious[:0], record.EventHash...)
	return nil
}

func finishAuditVerification(
	state *auditVerificationState,
	report *AuditIntegrityReport,
) {
	if !state.started {
		return
	}
	report.Heads = append(report.Heads, AuditChainHead{
		ClubID:   state.chainClubID,
		Position: state.expectedPosition - 1,
		Hash:     hex.EncodeToString(state.expectedPrevious),
	})
}

// VerifyAuditIntegrity checks every portal audit chain in position order. New
// version-2 records are recomputed from their stored canonical fields; legacy
// version-1 records are still checked for position and hash linkage.
func (store *Store) VerifyAuditIntegrity(
	ctx context.Context,
) (AuditIntegrityReport, error) {
	report := AuditIntegrityReport{
		VerifiedAt: store.now(),
		Heads:      []AuditChainHead{},
	}
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				id,
				club_id,
				actor_user_id,
				actor_kind,
				legacy_admin_user_id,
				COALESCE(acting_role_key, ''),
				action,
				target_type,
				COALESCE(target_id, ''),
				outcome,
				correlation_id,
				metadata,
				chain_position,
				previous_hash,
				event_hash,
				COALESCE(host(ip_address), ''),
				COALESCE(user_agent, ''),
				occurred_at,
				hash_version
			FROM portal_audit_events
			ORDER BY club_id NULLS FIRST, chain_position NULLS FIRST, id
		`)
		if err != nil {
			return fmt.Errorf("query portal audit chains: %w", err)
		}
		defer rows.Close()

		state := auditVerificationState{}
		for rows.Next() {
			var (
				record        auditStoredRecord
				clubID        pgtype.Int4
				actorUserID   pgtype.UUID
				legacyAdminID pgtype.Int4
				position      pgtype.Int8
				metadataJSON  []byte
			)
			if err := rows.Scan(
				&record.ID,
				&clubID,
				&actorUserID,
				&record.Event.ActorKind,
				&legacyAdminID,
				&record.Event.ActingRoleKey,
				&record.Event.Action,
				&record.Event.TargetType,
				&record.Event.TargetID,
				&record.Event.Outcome,
				&record.Event.CorrelationID,
				&metadataJSON,
				&position,
				&record.PreviousHash,
				&record.EventHash,
				&record.Event.IPAddress,
				&record.Event.UserAgent,
				&record.Event.OccurredAt,
				&record.HashVersion,
			); err != nil {
				return fmt.Errorf("scan portal audit chain: %w", err)
			}
			if clubID.Valid {
				value := clubID.Int32
				record.Event.ClubID = &value
				record.ChainClubID = &value
				record.ChainKey = fmt.Sprintf("club:%d", value)
			} else {
				record.ChainKey = "global"
			}
			if actorUserID.Valid {
				value := uuid.UUID(actorUserID.Bytes)
				record.Event.ActorUserID = &value
			}
			if legacyAdminID.Valid {
				value := legacyAdminID.Int32
				record.Event.LegacyAdminID = &value
			}
			if len(metadataJSON) == 0 {
				record.Event.Metadata = map[string]any{}
			} else {
				decoder := json.NewDecoder(bytes.NewReader(metadataJSON))
				decoder.UseNumber()
				if err := decoder.Decode(&record.Event.Metadata); err != nil {
					return &AuditIntegrityError{EventID: record.ID, Reason: "metadata is not valid JSON"}
				}
			}
			record.Position = position.Int64
			record.PositionValid = position.Valid
			if err := verifyAuditRecord(&state, record, &report); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate portal audit chains: %w", err)
		}
		finishAuditVerification(&state, &report)
		return nil
	})
	return report, err
}
