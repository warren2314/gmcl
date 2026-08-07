package httpserver

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseIneligibleQueueFiltersDefaultsAndRejectsUnknownValues(t *testing.T) {
	got := parseIneligibleQueueFilters(url.Values{
		"state":       {"not-a-state"},
		"origin":      {"untrusted"},
		"case_status": {"made-up"},
		"age":         {"500 years"},
		"player":      {"  A Player  "},
	})
	if got.State != "open" || got.Origin != "" || got.CaseStatus != "" || got.Age != "" || got.Scope != "mine" || got.Sort != "newest" {
		t.Fatalf("unexpected normalised filters: %#v", got)
	}
	if got.Player != "A Player" {
		t.Fatalf("player filter was not trimmed: %q", got.Player)
	}
}

func TestBuildIneligibleQueueQuerySupportsMyWorkAndOldestFirst(t *testing.T) {
	adminID := int32(42)
	query, args := buildIneligibleQueueQueryForAdmin(ineligibleQueueFilters{
		State: "all",
		Scope: "mine",
		Sort:  "oldest",
	}, &adminID)
	if !strings.Contains(query, "c.assigned_admin_id=$1") {
		t.Fatalf("my-work ownership filter missing: %s", query)
	}
	if !strings.Contains(query, "COALESCE(i.external_created_at,i.created_at) ASC,i.id ASC") {
		t.Fatalf("oldest-first ordering missing: %s", query)
	}
	if !reflect.DeepEqual(args, []any{adminID}) {
		t.Fatalf("query args = %#v, want admin id", args)
	}
}

func TestPlainIneligibleStatusExplainsWorkflowTerms(t *testing.T) {
	tests := map[string]string{
		"linked":            "Case raised",
		"response_pending":  "Waiting for response",
		"decision_proposed": "Decision ready for approval",
	}
	for value, want := range tests {
		if got := plainIneligibleStatus(value); got != want {
			t.Errorf("plainIneligibleStatus(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestBuildIneligibleQueueQueryUsesArgumentsForUserInput(t *testing.T) {
	filter := ineligibleQueueFilters{
		State:         "linked",
		Origin:        "google_form",
		ReportingClub: "Reporting CC",
		OffendingClub: "Offending CC",
		Team:          "1st XI",
		Player:        "A Player",
		Assignee:      "hussan",
		CaseStatus:    "investigating",
		Age:           "7d",
	}
	query, args := buildIneligibleQueueQuery(filter)
	for _, value := range []string{"Reporting CC", "Offending CC", "1st XI", "A Player", "hussan"} {
		if strings.Contains(query, value) {
			t.Fatalf("query interpolates user input %q: %s", value, query)
		}
	}
	want := []any{"linked", "google_form", "%Reporting CC%", "%Offending CC%", "%1st XI%", "%A Player%", "%hussan%", "investigating"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("query args = %#v, want %#v", args, want)
	}
	if !strings.Contains(query, "interval '7 days'") {
		t.Fatalf("validated age filter missing from query: %s", query)
	}
	if !strings.Contains(query, "sanction_intake_attachments") {
		t.Fatalf("queue query does not include evidence attachment counts: %s", query)
	}
}

func TestIneligibleAttachmentDispositionAllowsSafeMediaInline(t *testing.T) {
	for _, mediaType := range []string{"image/jpeg", "image/png", "image/webp", "image/gif; charset=binary", "video/mp4"} {
		if got := ineligibleAttachmentDisposition(mediaType, true); got != "inline" {
			t.Errorf("%s disposition = %q, want inline", mediaType, got)
		}
	}
	for _, mediaType := range []string{"image/heic", "image/svg+xml", "application/pdf", "text/html", "video/webm"} {
		if got := ineligibleAttachmentDisposition(mediaType, true); got != "attachment" {
			t.Errorf("%s disposition = %q, want attachment", mediaType, got)
		}
	}
	if got := ineligibleAttachmentPreviewKind("video/mp4; codecs=avc1"); got != "video" {
		t.Fatalf("MP4 preview kind = %q, want video", got)
	}
	if got := ineligibleAttachmentDisposition("image/jpeg", false); got != "attachment" {
		t.Fatalf("non-preview image disposition = %q, want attachment", got)
	}
}

func TestSourceStringFieldReadsExactGoogleFormHeaders(t *testing.T) {
	raw := []byte(`{
		"Email address":"reporter@example.com",
		"Name of defaulting player as shown on scorecard":"Alex Player",
		"Reason you believe the player is ineligible":"Not registered before the deadline",
		"Your Club":"Reporting CC",
		"Your Name & Role at Club/League":"Robin Reporter - Secretary",
		"Your Preferred tel no":"07123 456789"
	}`)
	tests := map[string]string{
		"email":  sourceStringField(raw, "Email address"),
		"player": sourceStringField(raw, "Name of defaulting player as shown on scorecard"),
		"reason": sourceStringField(raw, "Reason you believe the player is ineligible"),
		"club":   sourceStringField(raw, "Your Club"),
		"person": sourceStringField(raw, "Your Name & Role at Club/League"),
		"phone":  sourceStringField(raw, "Your Preferred tel no"),
	}
	want := map[string]string{
		"email":  "reporter@example.com",
		"player": "Alex Player",
		"reason": "Not registered before the deadline",
		"club":   "Reporting CC",
		"person": "Robin Reporter - Secretary",
		"phone":  "07123 456789",
	}
	if !reflect.DeepEqual(tests, want) {
		t.Fatalf("source fields = %#v, want %#v", tests, want)
	}
}

func TestIneligibleDefaultPublicSummaryIncludesFixture(t *testing.T) {
	date := time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC)
	got := ineligibleDefaultPublicSummary("Alex Player", &date)
	for _, want := range []string{"Alex Player", "04 August 2026"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q does not contain %q", got, want)
		}
	}
}

func TestReadIneligibleRetainedUploadConfinesStorageKey(t *testing.T) {
	root := t.TempDir()
	retained := filepath.Join(root, "sha256", "ab", "evidence.pdf")
	if err := os.MkdirAll(filepath.Dir(retained), 0700); err != nil {
		t.Fatal(err)
	}
	want := []byte("private retained evidence")
	if err := os.WriteFile(retained, want, 0600); err != nil {
		t.Fatal(err)
	}

	got, err := readIneligibleRetainedUpload(root, "sha256/ab/evidence.pdf")
	if err != nil {
		t.Fatalf("read valid retained upload: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("retained bytes = %q, want %q", got, want)
	}

	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte("must remain unreachable"), 0600); err != nil {
		t.Fatal(err)
	}
	relativeEscape, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	for _, storageKey := range []string{filepath.ToSlash(relativeEscape), filepath.ToSlash(outside)} {
		if _, err := readIneligibleRetainedUpload(root, storageKey); err == nil {
			t.Fatalf("escaping storage key %q was accepted", storageKey)
		}
	}
}

func TestReadIneligibleRetainedUploadRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte("must remain unreachable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.pdf")); err != nil {
		t.Skipf("symlinks are unavailable on this platform: %v", err)
	}
	if _, err := readIneligibleRetainedUpload(root, "escape.pdf"); err == nil {
		t.Fatal("symlink escaping the retained-upload root was accepted")
	}
}
