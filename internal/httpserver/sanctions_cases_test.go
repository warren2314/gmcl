package httpserver

import (
	"crypto/sha256"
	"fmt"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSanctionsSearchPatternsTreatAndAsAmpersand(t *testing.T) {
	for input, want := range map[string][]string{
		"Deane and Derby": {"%Deane and Derby%", "%deane & derby%"},
		"Deane & Derby":   {"%Deane & Derby%", "%deane and derby%"},
	} {
		if got := sanctionsSearchPatterns(input); !reflect.DeepEqual(got, want) {
			t.Errorf("sanctionsSearchPatterns(%q) = %#v, want %#v", input, got, want)
		}
	}
}

func TestSanctionsCategoryURLPreservesSearchAndClearsType(t *testing.T) {
	values := url.Values{"q": {"Deane and Derby"}, "season": {"2026"}, "type": {"fine"}}
	got := sanctionsCategoryURL(values, "yellow")
	want := "/sanctions?q=Deane+and+Derby&season=2026&view=yellow"
	if got != want {
		t.Fatalf("category URL = %q, want %q", got, want)
	}
}

func TestPublicEffectSubjectUsesEffectSpecificSubject(t *testing.T) {
	if got := publicEffectSubject("points_adjustment", "Second XI", "Case Player"); got != "Second XI" {
		t.Fatalf("points subject = %q", got)
	}
	if got := publicEffectSubject("player_ban", "Second XI", "Named Player"); got != "Named Player" {
		t.Fatalf("player-ban subject = %q", got)
	}
}

func TestTextContainsPrivateIdentity(t *testing.T) {
	if !textContainsPrivateIdentity("Allegation submitted by Example Cricket Club", "  Example   Cricket Club ") {
		t.Fatal("reporting-club identity was not detected")
	}
	if textContainsPrivateIdentity("Dear Club Secretary, please respond.", "Sec") {
		t.Fatal("short generic role must not create a false identity match")
	}
	if !textContainsPrivateIdentity("Reported by Jane Smith", "Jane Smith, Secretary") {
		t.Fatal("name portion of the legacy combined name/role value was not detected")
	}
}

func TestAdminCaseDecisionHTMLShowsProposedPunishment(t *testing.T) {
	points := 2
	starts := time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC)
	html := adminCaseDecisionHTML(adminCaseDecision{
		Status:        "proposed",
		Revision:      1,
		PublicReason:  "Failure to submit captain's report",
		RuleReference: "Penalty rule 3",
	}, []adminCaseEffect{{
		EffectType:         "red_card",
		Status:             "pending",
		Points:             &points,
		StartsAt:           &starts,
		CountsForTotting:   true,
		Explanation:        "Yellow card 3 converts to red card 2 with a 2-point deduction.",
		YellowBalanceAfter: "0",
		TeamRedCountAfter:  "2",
	}})
	for _, want := range []string{
		"Proposed punishment",
		"Red card",
		"2 points",
		"Yellow balance after",
		"Team red count after",
		"Counts towards card totting",
		"Penalty rule 3",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("decision HTML does not contain %q: %s", want, html)
		}
	}
}

func TestAdminCaseResponseHTMLShowsReplyProminentlyAndSafely(t *testing.T) {
	html := adminCaseResponseHTML(1176, `token"value`, adminCaseResponseView{
		ID:         42,
		EventType:  "party_response",
		ActorType:  "captain",
		ActorLabel: "Club Secretary",
		Body:       `<script>alert("reply")</script> We had permission.`,
		ReceivedAt: time.Date(2026, time.August, 13, 19, 30, 0, 0, time.UTC),
		Unreviewed: true,
	}, time.UTC)
	for _, want := range []string{
		`id="club-response"`,
		"Club reply received",
		"Needs review",
		"secure club portal",
		`&lt;script&gt;alert(&quot;reply&quot;)&lt;/script&gt; We had permission.`,
		`action="/admin/cases/1176/response-reviewed"`,
		`value="token&quot;value"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("response HTML missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `<script>`) {
		t.Fatalf("response body was not escaped: %s", html)
	}
}

func TestAdminCaseResponseHTMLHidesReviewActionAfterReview(t *testing.T) {
	html := adminCaseResponseHTML(1176, "csrf", adminCaseResponseView{
		ID:         42,
		EventType:  "external_response_recorded",
		ActorType:  "admin",
		Respondent: "secretary@example.test",
		Channel:    "email",
		Body:       "The club response",
		ReceivedAt: time.Now(),
	}, time.UTC)
	if !strings.Contains(html, "Reviewed") || strings.Contains(html, "response-reviewed") {
		t.Fatalf("reviewed response controls are wrong: %s", html)
	}
}

func TestAdminCaseNextStageShowsIndependentApprovalSequence(t *testing.T) {
	html := adminCaseNextStageHTML(true, false)
	for _, want := range []string{"Club reply reviewed", "published rule", "findings and sanctions", "Denver", "independent approver", "outcome emails"} {
		if !strings.Contains(html, want) {
			t.Fatalf("next-stage HTML missing %q: %s", want, html)
		}
	}
}
func TestParseAdminDecisionEffectsIgnoresValuesThatDoNotBelongToCard(t *testing.T) {
	effects := parseAdminDecisionEffects(url.Values{
		"effect_type":       {"red_card"},
		"fine_pounds":       {"25.00"},
		"points":            {"0"},
		"rescindable":       {"yes"},
		"trigger_condition": {"automatic test"},
	})
	if len(effects) != 1 {
		t.Fatalf("got %d effects, want 1", len(effects))
	}
	if effects[0].Points != nil || effects[0].AmountPence != nil {
		t.Fatalf("card retained unrelated manual values: %#v", effects[0])
	}
	if effects[0].Rescindable {
		t.Fatal("red card retained the yellow-card-only remedy")
	}
}

func TestParseAdminDecisionEffectsKeepsPointsAdjustmentValue(t *testing.T) {
	effects := parseAdminDecisionEffects(url.Values{
		"effect_type": {"points_adjustment"},
		"points":      {"-5"},
	})
	if len(effects) != 1 || effects[0].Points == nil || *effects[0].Points != -5 {
		t.Fatalf("points adjustment was not parsed: %#v", effects)
	}
}

func TestAdminDecisionEffectsHTMLExplainsManualPointsField(t *testing.T) {
	html := adminDecisionEffectsHTML(nil)
	if !strings.Contains(html, "Card deductions are calculated automatically from league policy after submission; do not enter them here.") {
		t.Fatalf("decision effects HTML does not explain the manual points field: %s", html)
	}
}
func TestAdminCaseAssignmentHidesDuplicateSelfAssignment(t *testing.T) {
	adminID := int32(42)
	html := adminCaseAssignmentHTML(152, "token", &adminID, "warren2314", &adminID)
	if !strings.Contains(html, "assigned to you") {
		t.Fatalf("self-assignment status missing: %s", html)
	}
	if strings.Contains(html, "assign-self") || strings.Contains(html, "<button") {
		t.Fatalf("self-assignment action should be hidden: %s", html)
	}
	if !sameAdminAssignment(&adminID, &adminID) {
		t.Fatal("duplicate assignment must be recognised as unchanged")
	}
}

func TestAdminCaseReporterHTMLShowsPrivateReporterDetailsSafely(t *testing.T) {
	html := adminCaseReporterHTML(adminCaseReporterView{
		Name:          `Jane <Reporter>`,
		Email:         `jane@example.test`,
		Role:          `Club Secretary`,
		Phone:         `07000 111222`,
		ReportingClub: `Example CC`,
	})
	for _, want := range []string{
		`Reported by`,
		`Private case information`,
		`Jane &lt;Reporter&gt;`,
		`mailto:jane@example.test`,
		`Club Secretary`,
		`07000 111222`,
		`Example CC`,
		`receives the reporting-side final outcome`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("reporter card missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `Jane <Reporter>`) {
		t.Fatalf("reporter identity was not escaped: %s", html)
	}
}
func TestAdminCaseAssignmentAllowsExplicitReassignment(t *testing.T) {
	assignedID := int32(7)
	currentID := int32(42)
	html := adminCaseAssignmentHTML(152, "token", &assignedID, "joe", &currentID)
	for _, want := range []string{"Current investigator:", "joe", "Reassign investigation to me", "/admin/cases/152/assign-self"} {
		if !strings.Contains(html, want) {
			t.Fatalf("reassignment HTML does not contain %q: %s", want, html)
		}
	}
	if sameAdminAssignment(&assignedID, &currentID) {
		t.Fatal("different investigators must not be treated as the same assignment")
	}
}

func TestRawEvidenceContainingReporterIdentityCannotBeSharedDirectly(t *testing.T) {
	html := adminEvidenceDisclosureControlsHTML(41, 72, "csrf-token", adminEvidenceDisclosureState{
		Available: true,
	})
	for _, required := range []string{
		"Private source evidence",
		`value="create_redacted_derivative"`,
		`name="reviewer_attestation"`,
		"reporter name, role, email, phone and reporting-club identity have been removed",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("raw evidence controls do not contain %q: %s", required, html)
		}
	}
	for _, forbidden := range []string{`value="shared"`, "Share reviewed derivative"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("raw evidence containing reporter identity exposed direct sharing control %q: %s", forbidden, html)
		}
	}
}

func TestReviewedRedactedDerivativeCanBeShared(t *testing.T) {
	sourceID := int64(72)
	reviewedAt := time.Date(2026, time.August, 5, 9, 30, 0, 0, time.UTC)
	html := adminEvidenceDisclosureControlsHTML(41, 73, "csrf-token", adminEvidenceDisclosureState{
		SourceEvidenceID: &sourceID,
		Reviewer:         "privacy-reviewer",
		ReviewedAt:       &reviewedAt,
		Eligible:         true,
		Available:        true,
	})
	for _, required := range []string{
		"Reviewed redacted derivative of evidence #72",
		"privacy-reviewer",
		`value="shared"`,
		"Share reviewed derivative",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("reviewed derivative controls do not contain %q: %s", required, html)
		}
	}
	if strings.Contains(html, `value="create_redacted_derivative"`) {
		t.Fatalf("a derivative must not be offered as the source of another derivative: %s", html)
	}
}

func TestOffendingClubPortalQueriesRequireReviewedDerivativeProjection(t *testing.T) {
	for name, query := range map[string]string{
		"list":     portalSharedEvidenceListQuery,
		"download": portalSharedEvidenceDownloadQuery,
	} {
		if !strings.Contains(query, "FROM sanction_offending_club_evidence_derivatives allowed") {
			t.Fatalf("%s query can bypass the reviewed derivative projection: %s", name, query)
		}
		if strings.Contains(query, "NOT EXISTS(SELECT 1 FROM sanction_case_intake_evidence") {
			t.Fatalf("%s query retained the old direct-source fallback: %s", name, query)
		}
	}
}

func TestRedactedEvidenceMigrationEnforcesAttestationAndRevokesRawShares(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0066_redacted_evidence_derivatives.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := string(raw)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS sanction_case_evidence_derivatives",
		evidenceRedactionAttestationCode,
		"source_sha256<>derivative_sha256",
		"CREATE OR REPLACE VIEW sanction_offending_club_evidence_derivatives",
		"Automatically revoked: offending-club disclosure now requires a reviewed redacted derivative",
		"CREATE TRIGGER trg_sanction_evidence_share_requires_derivative",
	} {
		if !strings.Contains(migration, required) {
			t.Fatalf("redacted-evidence migration is missing %q", required)
		}
	}
}

func TestReviewedDerivativeChecksumBlocksRawSourceSubstitution(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("SANCTIONS_EVIDENCE_DIR", directory)
	key := "reviewed-derivative"
	path := filepath.Join(directory, key)
	redacted := []byte("Redacted fixture evidence with no reporter identity")
	digest := fmt.Sprintf("%x", sha256.Sum256(redacted))
	if err := os.WriteFile(path, redacted, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readVerifiedCaseEvidence(key, digest); err != nil || string(got) != string(redacted) {
		t.Fatalf("reviewed derivative was not readable: %q, %v", got, err)
	}

	rawSource := []byte("Reporter Jane Smith, secretary, jane@example.test, 07000111222, Example CC")
	if err := os.WriteFile(path, rawSource, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readVerifiedCaseEvidence(key, digest); err == nil {
		t.Fatal("raw source containing reporter identity replaced the reviewed derivative without being rejected")
	}
}

func TestCopyEvidenceUsesDetectedContentType(t *testing.T) {
	t.Setenv("SANCTIONS_EVIDENCE_DIR", t.TempDir())
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 504)...)
	file, err := os.CreateTemp(t.TempDir(), "evidence-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.Write(png); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	header := &multipart.FileHeader{Size: int64(len(png))}
	header.Header = map[string][]string{"Content-Type": {"application/x-msdownload"}}
	_, _, size, media, err := copyEvidence(file, header)
	if err != nil {
		t.Fatalf("detected PNG should be accepted despite an untrusted browser label: %v", err)
	}
	if media != "image/png" || size != int64(len(png)) {
		t.Fatalf("copyEvidence returned media=%q size=%d", media, size)
	}
}

func TestCopyEvidenceAcceptsDetectedMP4(t *testing.T) {
	t.Setenv("SANCTIONS_EVIDENCE_DIR", t.TempDir())
	mp4 := append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), make([]byte, 488)...)
	file, err := os.CreateTemp(t.TempDir(), "evidence-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.Write(mp4); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	header := &multipart.FileHeader{Size: int64(len(mp4))}
	header.Header = map[string][]string{"Content-Type": {"application/octet-stream"}}
	_, _, size, media, err := copyEvidence(file, header)
	if err != nil {
		t.Fatalf("detected MP4 should be accepted: %v", err)
	}
	if media != "video/mp4" || size != int64(len(mp4)) {
		t.Fatalf("copyEvidence returned media=%q size=%d", media, size)
	}
}

func TestCopyEvidenceRejectsSpoofedImageAndSVG(t *testing.T) {
	for name, data := range map[string][]byte{
		"executable": []byte("MZ\x90\x00not really a png"),
		"fake-mp4":   []byte("\x00\x00\x00\x18ftypEVIL\x00\x00\x00\x00payload"),
		"svg":        []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SANCTIONS_EVIDENCE_DIR", t.TempDir())
			file, err := os.CreateTemp(t.TempDir(), "evidence-*")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err = file.Write(data); err != nil {
				t.Fatal(err)
			}
			if _, err = file.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			header := &multipart.FileHeader{Size: int64(len(data))}
			header.Header = map[string][]string{"Content-Type": {"image/png"}}
			if _, _, _, _, err = copyEvidence(file, header); err == nil {
				t.Fatalf("%s content spoofed as image/png was accepted", name)
			}
		})
	}
}

func TestAdminCaseSourceReportShowsRequiredFieldsSafely(t *testing.T) {
	report := adminCaseSourceReport{IntakeID: 77, OffendingClub: "Offending CC", Team: "3rd XI", FixtureDate: "2026-08-01", Player: "Alex Player", Reason: "Registration <missing>", AdditionalInfo: "Checked list", AdditionalEvidence: "https://example.test/evidence", Score: "Scorecard 123"}
	html := adminCaseSourceReportHTML(report)
	for _, want := range []string{"Original report details", "Offending CC", "3rd XI", "2026-08-01", "Alex Player", "Registration &lt;missing&gt;", "Checked list", "https://example.test/evidence", "Scorecard 123", "/admin/ineligible/77"} {
		if !strings.Contains(html, want) {
			t.Fatalf("source report card missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "Registration <missing>") {
		t.Fatal("source report content was not escaped")
	}
}
