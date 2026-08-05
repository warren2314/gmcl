package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ineligibledomain "cricket-ground-feedback/internal/ineligible"

	"github.com/go-chi/chi/v5"
)

func (s *Server) nativeIneligibleFormFields(r *http.Request) string {
	rawID, _, err := newPublicToken()
	if err != nil {
		rawID = fmt.Sprintf("fallback_%d_native_submission", time.Now().UnixNano())
	}
	var clubOptions strings.Builder
	rows, queryErr := s.DB.Query(r.Context(), `SELECT id,name FROM clubs ORDER BY name`)
	if queryErr == nil {
		defer rows.Close()
		for rows.Next() {
			var id int
			var name string
			if rows.Scan(&id, &name) == nil {
				fmt.Fprintf(&clubOptions, `<option value="%d">%s</option>`, id, escapeHTML(name))
			}
		}
	}
	return fmt.Sprintf(`<input type="hidden" name="submission_id" value="%s"><input type="hidden" name="submission_timestamp" value="%s">
<div class="col-12" id="ineligible-player-fields">
  <div class="border rounded p-3 bg-light-subtle">
    <h2 class="h6 mb-1">Ineligible-player report details</h2>
    <p class="small text-muted">These details enter the league's private triage queue. Submitting does not contact the other club or issue a sanction.</p>
    <div class="row g-3">
      <div class="col-md-6"><label class="form-label">Your role at the club</label><input class="form-control" name="reporter_role" maxlength="200" data-ineligible-required></div>
      <div class="col-md-6"><label class="form-label">Preferred telephone number</label><input class="form-control" type="tel" name="reporter_phone" maxlength="100" data-ineligible-required></div>
      <div class="col-md-6"><label class="form-label">Your club</label><select class="form-select" name="reporting_club_id" data-ineligible-required><option value="">Choose your club...</option>%s</select></div>
      <div class="col-md-6"><label class="form-label">Score or scorecard reference (optional)</label><input class="form-control" name="score" maxlength="500"></div>
      <div class="col-12"><label class="form-label">Additional information (optional)</label><textarea class="form-control" name="additional_info" rows="3" maxlength="10000"></textarea></div>
      <div class="col-12"><label class="form-label">Additional evidence or links (optional)</label><textarea class="form-control" name="additional_evidence" rows="3" maxlength="10000"></textarea></div>
    </div>
  </div>
</div>`, escapeHTML(rawID), time.Now().UTC().Format(time.RFC3339Nano), clubOptions.String())
}

func (s *Server) stageNativeIneligibleReport(w http.ResponseWriter, r *http.Request, reporterName, reporterEmail, reason string, teamID int, offendingClub, team string) {
	submittedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(r.FormValue("submission_timestamp")))
	now := time.Now().UTC()
	if err != nil || submittedAt.Before(now.Add(-90*24*time.Hour)) || submittedAt.After(now.Add(5*time.Minute)) {
		http.Error(w, "this report form has expired; please reload it and try again", http.StatusBadRequest)
		return
	}
	fixtureDate, err := time.Parse("2006-01-02", strings.TrimSpace(r.FormValue("match_date")))
	if err != nil {
		http.Error(w, "a valid fixture date is required for an ineligible-player report", http.StatusBadRequest)
		return
	}
	reportingClubID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("reporting_club_id")))
	if err != nil || reportingClubID <= 0 {
		http.Error(w, "your reporting club is required", http.StatusBadRequest)
		return
	}
	var reportingClub string
	if err = s.DB.QueryRow(r.Context(), `SELECT name FROM clubs WHERE id=$1`, reportingClubID).Scan(&reportingClub); err != nil {
		http.Error(w, "reporting club not found", http.StatusBadRequest)
		return
	}

	role := strings.TrimSpace(r.FormValue("reporter_role"))
	phone := strings.TrimSpace(r.FormValue("reporter_phone"))
	player := strings.TrimSpace(r.FormValue("player_name"))
	additionalInfo := strings.TrimSpace(r.FormValue("additional_info"))
	additionalEvidence := strings.TrimSpace(r.FormValue("additional_evidence"))
	score := strings.TrimSpace(r.FormValue("score"))
	if role == "" || phone == "" || player == "" {
		http.Error(w, "your role, telephone number, and player name are required", http.StatusBadRequest)
		return
	}
	if !nativeIneligibleLengthsValid(reporterName, reporterEmail, role, phone, reportingClub, offendingClub, team, player, reason, additionalInfo, additionalEvidence, score) {
		http.Error(w, "one or more fields exceed the permitted length", http.StatusBadRequest)
		return
	}

	var evidence *ineligibledomain.NativeEvidence
	newEvidencePath := ""
	if file, header, fileErr := r.FormFile("evidence"); fileErr == nil {
		defer file.Close()
		key, sum, size, media, copyErr := copyEvidence(file, header)
		if copyErr != nil {
			http.Error(w, copyErr.Error(), http.StatusBadRequest)
			return
		}
		newEvidencePath = filepath.Join(evidenceDir(), filepath.Base(key))
		evidence = &ineligibledomain.NativeEvidence{
			OriginalName: filepath.Base(header.Filename), MediaType: media,
			ByteSize: size, SHA256: sum, StorageKey: key,
		}
	} else if fileErr != http.ErrMissingFile {
		http.Error(w, "could not read the evidence upload", http.StatusBadRequest)
		return
	}
	keepEvidence := false
	if newEvidencePath != "" {
		defer func() {
			if !keepEvidence {
				if removeErr := os.Remove(newEvidencePath); removeErr != nil && !os.IsNotExist(removeErr) {
					slog.Warn("remove redundant native intake evidence", "path", newEvidencePath, "error", removeErr)
				}
			}
		}()
	}

	result, err := ineligibledomain.NewPGStore(s.DB).StageNative(r.Context(), ineligibledomain.NativeSubmission{
		SubmissionID:       strings.TrimSpace(r.FormValue("submission_id")),
		SubmittedAt:        submittedAt,
		ReporterEmail:      reporterEmail,
		ReporterName:       reporterName,
		ReporterRole:       role,
		ReporterPhone:      phone,
		ReportingClub:      reportingClub,
		OffendingClub:      offendingClub,
		Team:               team,
		TeamID:             teamID,
		Player:             player,
		FixtureDate:        fixtureDate,
		Reason:             reason,
		AdditionalInfo:     additionalInfo,
		AdditionalEvidence: additionalEvidence,
		Score:              score,
		Evidence:           evidence,
	})
	if err != nil {
		slog.Error("stage native ineligible-player intake", "error", err, "request_id", requestID(r))
		http.Error(w, "could not retain this report; please try again", http.StatusInternalServerError)
		return
	}
	keepEvidence = result.Disposition != "unchanged"

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageHead(w, "Ineligible-player report received")
	writeCaptainNav(w)
	fmt.Fprintf(w, `<main class="container py-5" style="max-width:680px"><div class="alert alert-success"><h1 class="h4">Report received for private triage</h1><p>Your report has been safely recorded. No club has been contacted and no sanction has been issued.</p><p>If the league opens an investigation, authorised staff will manage correspondence and the decision through the sanctions workflow. Once an independently approved outcome is available, the reporting club will receive the findings at its official club mailbox.</p><p class="mb-0">Intake reference: <strong>%s</strong></p></div></main>`, escapeHTML(result.Reference))
	pageFooter(w)
}

func nativeIneligibleLengthsValid(values ...string) bool {
	limits := []int{150, 320, 200, 100, 200, 200, 200, 200, 10000, 10000, 10000, 500}
	if len(values) != len(limits) {
		return false
	}
	for index, value := range values {
		if len([]rune(value)) > limits[index] {
			return false
		}
	}
	return true
}

func nativeIntakeEvidenceLinks(rawJSON []byte, intakeID int64, revision int) string {
	files := nativeIntakeEvidence(rawJSON)
	if len(files) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString(`<section class="card mb-4"><div class="card-header fw-semibold">Retained source files</div><ul class="list-group list-group-flush">`)
	for _, file := range files {
		fmt.Fprintf(&out, `<li class="list-group-item d-flex justify-content-between gap-3"><span><strong>%s</strong><br><span class="small text-muted">%s; %d bytes; SHA-256 %s</span></span><a class="btn btn-sm btn-outline-primary align-self-center" href="/admin/ineligible/%d/native-evidence/%d/%s">Download</a></li>`, escapeHTML(filepath.Base(file.OriginalName)), escapeHTML(file.MediaType), file.ByteSize, escapeHTML(file.SHA256), intakeID, revision, escapeHTML(file.SHA256))
	}
	out.WriteString(`</ul></section>`)
	return out.String()
}

func nativeIntakeEvidence(rawJSON []byte) []ineligibledomain.NativeEvidence {
	var raw map[string]json.RawMessage
	if json.Unmarshal(rawJSON, &raw) != nil {
		return nil
	}
	var files []ineligibledomain.NativeEvidence
	if value := raw["File Upload"]; len(value) > 0 {
		_ = json.Unmarshal(value, &files)
	}
	return files
}

func (s *Server) handleAdminNativeIntakeEvidenceDownload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		intakeID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || intakeID <= 0 {
			http.NotFound(w, r)
			return
		}
		revision, err := strconv.Atoi(chi.URLParam(r, "revision"))
		if err != nil || revision <= 0 {
			http.NotFound(w, r)
			return
		}
		requestedSHA := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "sha")))
		if len(requestedSHA) != 64 {
			http.NotFound(w, r)
			return
		}
		var rawJSON []byte
		err = s.DB.QueryRow(r.Context(), `
			SELECT r.raw_data FROM sanction_intake_revisions r
			JOIN sanction_intakes i ON i.id=r.intake_id
			WHERE r.intake_id=$1 AND r.revision=$2 AND i.origin='native_form'
		`, intakeID, revision).Scan(&rawJSON)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var selected *ineligibledomain.NativeEvidence
		for _, file := range nativeIntakeEvidence(rawJSON) {
			if strings.EqualFold(file.SHA256, requestedSHA) {
				copy := file
				selected = &copy
				break
			}
		}
		if selected == nil || selected.StorageKey == "" || filepath.Base(selected.StorageKey) != selected.StorageKey {
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile(filepath.Join(evidenceDir(), selected.StorageKey))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		digest := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(digest[:]), selected.SHA256) {
			slog.Error("native intake evidence checksum mismatch", "intake_id", intakeID, "revision", revision, "storage_key", selected.StorageKey)
			http.Error(w, "retained evidence failed its integrity check", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", selected.MediaType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, strings.ReplaceAll(filepath.Base(selected.OriginalName), `"`, "")))
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	}
}
