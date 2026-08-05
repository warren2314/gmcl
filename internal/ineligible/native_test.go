package ineligible

import (
	"strings"
	"testing"
	"time"
)

func validNativeSubmission() NativeSubmission {
	return NativeSubmission{
		SubmissionID:       "PjW4e8oVLpR1Ue6Nhk3cYx2_A9sQm7Kt",
		SubmittedAt:        time.Date(2026, time.August, 4, 20, 30, 0, 0, time.UTC),
		ReporterEmail:      " Reporter@Example.COM ",
		ReporterName:       "Sam Reporter",
		ReporterRole:       "Club secretary",
		ReporterPhone:      "07700 900123",
		ReportingClub:      "Reporter CC",
		OffendingClub:      "Offending CC",
		Team:               "Offending CC 2nd XI",
		TeamID:             42,
		Player:             "Alex Player",
		FixtureDate:        time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
		Reason:             "The player was not registered before the deadline.",
		AdditionalInfo:     "Registration list checked on match day.",
		AdditionalEvidence: "https://example.test/scorecard/123",
		Score:              "Offending CC 184-7",
		Evidence: &NativeEvidence{
			OriginalName: "registration.pdf", MediaType: "application/pdf",
			ByteSize: 1234, SHA256: strings.Repeat("a", 64), StorageKey: "copy-one",
		},
	}
}

func TestPrepareNativeSubmissionMirrorsReviewedFormContract(t *testing.T) {
	prepared, err := prepareNativeSubmission(validNativeSubmission())
	if err != nil {
		t.Fatalf("prepare native submission: %v", err)
	}
	for _, header := range DefaultGoogleFormSchema().Headers {
		if _, exists := prepared.RawData[header]; !exists {
			t.Errorf("immutable raw data is missing reviewed header %q", header)
		}
	}
	if got := prepared.RawData["Email address"]; got != "reporter@example.com" {
		t.Fatalf("normalised email = %#v", got)
	}
	if got := prepared.RawData["Offending Club's Name"]; got != "Offending CC" {
		t.Fatalf("offending club = %#v", got)
	}
	if got := prepared.RawData["Your Club"]; got != "Reporter CC" {
		t.Fatalf("reporting club = %#v", got)
	}
	if prepared.ReportingClub == prepared.OffendingClub {
		t.Fatal("reporting and offending club projections were conflated")
	}
	if prepared.ExternalKey == "" || prepared.RawSHA256 == "" || prepared.HeaderSHA256 == "" {
		t.Fatal("native provenance digests must be populated")
	}
}

func TestPrepareNativeSubmissionIsIdempotentAcrossTemporaryUploadCopies(t *testing.T) {
	first := validNativeSubmission()
	second := validNativeSubmission()
	second.Evidence.StorageKey = "different-temporary-copy"

	a, err := prepareNativeSubmission(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := prepareNativeSubmission(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.ExternalKey != b.ExternalKey {
		t.Fatal("one browser submission must retain one stable intake identity")
	}
	if a.RawSHA256 != b.RawSHA256 {
		t.Fatal("temporary storage keys must not create false source revisions")
	}

	second.Reason = "Updated allegation after checking the registration date."
	c, err := prepareNativeSubmission(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.ExternalKey != c.ExternalKey {
		t.Fatal("changed content must remain attached to the same intake")
	}
	if a.RawSHA256 == c.RawSHA256 {
		t.Fatal("changed content must append a new revision")
	}
}

func TestPrepareNativeSubmissionFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(*NativeSubmission)
		want string
	}{
		{name: "missing id", edit: func(s *NativeSubmission) { s.SubmissionID = "" }, want: "submission id"},
		{name: "missing role", edit: func(s *NativeSubmission) { s.ReporterRole = "" }, want: "reporter role"},
		{name: "missing fixture", edit: func(s *NativeSubmission) { s.FixtureDate = time.Time{} }, want: "fixture date"},
		{name: "invalid email", edit: func(s *NativeSubmission) { s.ReporterEmail = "not-an-email" }, want: "email is invalid"},
		{name: "unmapped team", edit: func(s *NativeSubmission) { s.TeamID = 0 }, want: "team id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			submission := validNativeSubmission()
			test.edit(&submission)
			_, err := prepareNativeSubmission(submission)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want text %q", err, test.want)
			}
		})
	}
}
