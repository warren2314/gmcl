package httpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func ineligibleTestURLParam(request *http.Request, key, value string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add(key, value)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func TestIneligibleMergeLinkFormRequiresMappedTeam(t *testing.T) {
	request := httptest.NewRequest("POST", "/admin/ineligible/12/link-case", strings.NewReader(url.Values{
		"case_id": {"34"}, "reason": {"Same fixture and player"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request = ineligibleTestURLParam(request, "id", "12")
	if _, _, _, _, _, ok := ineligibleMergeLinkForm(request); ok {
		t.Fatal("form without an explicitly mapped team was accepted")
	}
}

func TestIneligibleMergeLinkFormAcceptsLeagueOriginWithoutClub(t *testing.T) {
	request := httptest.NewRequest("POST", "/admin/ineligible/12/link-case", strings.NewReader(url.Values{
		"case_id": {"34"}, "team_id": {"56"}, "reason": {"Same fixture and player"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request = ineligibleTestURLParam(request, "id", "12")
	intakeID, caseID, teamID, clubID, reason, ok := ineligibleMergeLinkForm(request)
	if !ok || intakeID != 12 || caseID != 34 || teamID != 56 || clubID != nil || reason == "" {
		t.Fatalf("unexpected parsed link: intake=%d case=%d team=%d club=%v reason=%q ok=%v", intakeID, caseID, teamID, clubID, reason, ok)
	}
}

func TestIneligibleCaseAcceptsIntakeOnlyBeforeResponseOrDecision(t *testing.T) {
	for _, test := range []struct {
		status string
		want   bool
	}{
		{"submitted", true}, {"triage", true}, {"investigating", true},
		{"response_pending", false}, {"decision_proposed", false}, {"approved", false},
		{"published", false}, {"appealed", false}, {"closed", false}, {"withdrawn", false},
	} {
		if got := ineligibleCaseAcceptsIntake(test.status); got != test.want {
			t.Errorf("status %q: accepts=%v, want %v", test.status, got, test.want)
		}
	}
}

func TestWriteIneligibleLinkCaseFormRequiresExternalReportingClub(t *testing.T) {
	output := httptest.NewRecorder()
	writeIneligibleLinkCaseForm(output, "csrf", 12, "Example CC", []ineligibleClubOption{{ID: 9, Name: "Example CC"}}, []ineligibleTeamOption{{ID: 8, ClubName: "Other CC", TeamName: "1st XI"}}, []ineligibleCaseOption{{ID: 7, Reference: "SC-7"}})
	html := output.Body.String()
	for _, expected := range []string{`name="team_id" required`, `name="reporting_club_id" required`, `value="9" selected`, "Link and merge intake", "No notice is queued"} {
		if !strings.Contains(html, expected) {
			t.Errorf("link form missing %q", expected)
		}
	}
}

func TestWriteIneligibleLinkCaseFormOnlyExactLeagueOriginIsOptional(t *testing.T) {
	for _, test := range []struct {
		reported string
		required bool
	}{{"GMCL Official", false}, {"gmcl official", true}, {" GMCL Official ", true}} {
		output := httptest.NewRecorder()
		writeIneligibleLinkCaseForm(output, "csrf", 12, test.reported, nil, nil, nil)
		hasRequired := strings.Contains(output.Body.String(), `name="reporting_club_id" required`)
		if hasRequired != test.required {
			t.Errorf("reported %q: required=%v, want %v", test.reported, hasRequired, test.required)
		}
	}
}

func TestVerifyAndRetainGoogleCaseEvidence(t *testing.T) {
	uploadDirectory := t.TempDir()
	caseDirectory := t.TempDir()
	t.Setenv("INELIGIBLE_UPLOAD_DIR", uploadDirectory)
	t.Setenv("SANCTIONS_EVIDENCE_DIR", caseDirectory)
	data := []byte("immutable evidence")
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	storageKey := filepath.Join("sha256", digest[:2], digest)
	path := filepath.Join(uploadDirectory, storageKey)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	item := retainedIntakeEvidence{RevisionID: 3, Kind: "google_drive", SourceKey: "drive-file-id", Name: "evidence.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: digest, StorageKey: filepath.ToSlash(storageKey)}
	key, err := verifyAndRetainCaseEvidence(item)
	if err != nil {
		t.Fatal(err)
	}
	if key != "intake-"+digest {
		t.Fatalf("storage key = %q", key)
	}
	retained, err := os.ReadFile(filepath.Join(caseDirectory, key))
	if err != nil || string(retained) != string(data) {
		t.Fatalf("retained evidence = %q, err=%v", retained, err)
	}
	// The content-addressed copy is idempotent and verified on reuse.
	if second, err := verifyAndRetainCaseEvidence(item); err != nil || second != key {
		t.Fatalf("second retain = %q, %v", second, err)
	}
}

func TestVerifyAndRetainEvidenceFailsClosed(t *testing.T) {
	uploadDirectory := t.TempDir()
	t.Setenv("INELIGIBLE_UPLOAD_DIR", uploadDirectory)
	t.Setenv("SANCTIONS_EVIDENCE_DIR", t.TempDir())
	data := []byte("tampered")
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("expected")))
	storageKey := filepath.Join("sha256", digest[:2], digest)
	path := filepath.Join(uploadDirectory, storageKey)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	item := retainedIntakeEvidence{RevisionID: 1, Kind: "google_drive", SourceKey: "drive-file-id", Name: "evidence.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: digest, StorageKey: filepath.ToSlash(storageKey)}
	if _, err := verifyAndRetainCaseEvidence(item); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
	item.StorageKey = "../escape"
	if _, err := verifyAndRetainCaseEvidence(item); err == nil || !strings.Contains(strings.ToLower(err.Error()), "path") && !strings.Contains(strings.ToLower(err.Error()), "storage") {
		t.Fatalf("expected path failure, got %v", err)
	}
}
