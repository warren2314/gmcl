package portal

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizeAuditTimeMatchesPostgreSQLPrecision(t *testing.T) {
	input := time.Date(2026, 7, 27, 12, 34, 56, 123456789, time.FixedZone("BST", 3600))
	got := normalizeAuditTime(input)
	if got.Location() != time.UTC {
		t.Fatalf("location = %s", got.Location())
	}
	if got.Nanosecond() != 123456000 {
		t.Fatalf("nanoseconds = %d", got.Nanosecond())
	}
}

func TestVersionTwoAuditChainCanBeFullyRecomputed(t *testing.T) {
	event := AuditEvent{
		ActorKind:     "system",
		Action:        "portal.preflight.tested",
		TargetType:    "portal_preflight",
		Outcome:       "success",
		CorrelationID: "audit-test",
		Metadata:      map[string]any{"count": int64(9007199254740993)},
		IPAddress:     "2001:0db8:0000:0000:0000:0000:0000:0001",
		UserAgent:     "  GMCL Test Browser  ",
		OccurredAt:    time.Date(2026, 7, 27, 12, 0, 0, 123456000, time.UTC),
	}
	event.IPAddress = normalizeAuditIPAddress(event.IPAddress)
	event.UserAgent = normalizeAuditUserAgent(event.UserAgent)
	first := signedAuditTestRecord(t, event, 1, nil)
	secondEvent := event
	secondEvent.Action = "portal.preflight.retested"
	secondEvent.CorrelationID = "audit-test-2"
	second := signedAuditTestRecord(t, secondEvent, 2, first.EventHash)

	var report AuditIntegrityReport
	var state auditVerificationState
	if err := verifyAuditRecord(&state, first, &report); err != nil {
		t.Fatal(err)
	}
	if err := verifyAuditRecord(&state, second, &report); err != nil {
		t.Fatal(err)
	}
	finishAuditVerification(&state, &report)
	if report.EventsChecked != 2 || report.FullyVerifiedEvents != 2 ||
		report.LegacyHashEvents != 0 || report.ChainsChecked != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if len(report.Heads) != 1 ||
		report.Heads[0].Position != 2 ||
		report.Heads[0].Hash == "" {
		t.Fatalf("unexpected chain head: %#v", report.Heads)
	}
}

func TestAuditVerificationRejectsCanonicalFieldTampering(t *testing.T) {
	event := AuditEvent{
		ActorKind:     "system",
		Action:        "portal.preflight.tested",
		TargetType:    "portal_preflight",
		Outcome:       "success",
		CorrelationID: "audit-tamper-test",
		Metadata:      map[string]any{},
		OccurredAt:    time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
	}
	record := signedAuditTestRecord(t, event, 1, nil)
	for _, mutate := range []func(*auditStoredRecord){
		func(record *auditStoredRecord) {
			record.Event.Action = "portal.preflight.changed"
		},
		func(record *auditStoredRecord) {
			record.Event.IPAddress = "192.0.2.99"
		},
		func(record *auditStoredRecord) {
			record.Event.UserAgent = "tampered browser"
		},
	} {
		tampered := record
		mutate(&tampered)
		var integrityErr *AuditIntegrityError
		if err := verifyAuditRecord(
			&auditVerificationState{},
			tampered,
			&AuditIntegrityReport{},
		); !errors.As(err, &integrityErr) ||
			!strings.Contains(integrityErr.Reason, "canonical event data") {
			t.Fatalf("tampering error = %v", err)
		}
	}
}

func TestAuditNetworkFieldsAreCanonicalizedBeforeHashing(t *testing.T) {
	if got := normalizeAuditIPAddress(
		" 2001:0db8:0000:0000:0000:0000:0000:0001 ",
	); got != "2001:db8::1" {
		t.Fatalf("canonical IP = %q", got)
	}
	if got := normalizeAuditIPAddress("not-an-address"); got != "" {
		t.Fatalf("invalid IP = %q", got)
	}
	longAgent := strings.Repeat("a", 600)
	if got := normalizeAuditUserAgent("  browser  "); got != "browser" {
		t.Fatalf("canonical user agent = %q", got)
	}
	if got := normalizeAuditUserAgent(longAgent); len(got) != 512 {
		t.Fatalf("canonical user agent length = %d", len(got))
	}
}

func TestAuditVerificationRejectsPositionGapAndPreviousHashMismatch(t *testing.T) {
	event := AuditEvent{
		ActorKind:     "system",
		Action:        "portal.preflight.tested",
		TargetType:    "portal_preflight",
		Outcome:       "success",
		CorrelationID: "audit-gap-test",
		Metadata:      map[string]any{},
		OccurredAt:    time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
	}
	t.Run("position gap", func(t *testing.T) {
		record := signedAuditTestRecord(t, event, 2, nil)
		err := verifyAuditRecord(
			&auditVerificationState{},
			record,
			&AuditIntegrityReport{},
		)
		if err == nil || !strings.Contains(err.Error(), "expected chain position") {
			t.Fatalf("position error = %v", err)
		}
	})
	t.Run("previous hash mismatch", func(t *testing.T) {
		record := signedAuditTestRecord(t, event, 1, bytes.Repeat([]byte{1}, sha256.Size))
		err := verifyAuditRecord(
			&auditVerificationState{},
			record,
			&AuditIntegrityReport{},
		)
		if err == nil || !strings.Contains(err.Error(), "previous hash") {
			t.Fatalf("previous-hash error = %v", err)
		}
	})
}

func TestLegacyAuditRecordIsLinkVerifiedWithoutFalseRecomputeClaim(t *testing.T) {
	record := auditStoredRecord{
		ID:            uuid.New(),
		EventHash:     bytes.Repeat([]byte{2}, sha256.Size),
		HashVersion:   1,
		Position:      1,
		PositionValid: true,
		ChainKey:      "global",
	}
	var report AuditIntegrityReport
	if err := verifyAuditRecord(
		&auditVerificationState{},
		record,
		&report,
	); err != nil {
		t.Fatal(err)
	}
	if report.LegacyHashEvents != 1 || report.FullyVerifiedEvents != 0 {
		t.Fatalf("legacy report = %#v", report)
	}
}

func signedAuditTestRecord(
	t *testing.T,
	event AuditEvent,
	position int64,
	previousHash []byte,
) auditStoredRecord {
	t.Helper()
	event.OccurredAt = normalizeAuditTime(event.OccurredAt)
	id := uuid.New()
	canonical, err := canonicalAuditBytes(
		id,
		event,
		position,
		previousHash,
		currentAuditHashVersion,
	)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return auditStoredRecord{
		ID:            id,
		Event:         event,
		Position:      position,
		PositionValid: true,
		PreviousHash:  append([]byte(nil), previousHash...),
		EventHash:     digest[:],
		HashVersion:   currentAuditHashVersion,
		ChainKey:      "global",
	}
}
