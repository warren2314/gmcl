package ineligible

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type fakeSource struct {
	data           SheetData
	err            error
	downloads      map[string]fakeUpload
	downloadErrors map[string]error
	downloadCalls  []string
}

func (s *fakeSource) Fetch(context.Context) (SheetData, error) { return s.data, s.err }

type fakeUpload struct {
	name         string
	contentType  string
	data         []byte
	declaredSize int64
}

func (s *fakeSource) DownloadUpload(_ context.Context, fileID string, maxBytes int64) (UploadDownload, error) {
	s.downloadCalls = append(s.downloadCalls, fileID)
	if err := s.downloadErrors[fileID]; err != nil {
		return UploadDownload{}, err
	}
	upload, ok := s.downloads[fileID]
	if !ok {
		return UploadDownload{}, fmt.Errorf("Google Drive file %s is inaccessible", fileID)
	}
	size := upload.declaredSize
	if size == 0 {
		size = int64(len(upload.data))
	}
	if size > maxBytes {
		return UploadDownload{}, fmt.Errorf("Google Drive file %s exceeds the configured byte limit", fileID)
	}
	return UploadDownload{
		DriveFileID: fileID, Name: upload.name, ContentType: upload.contentType,
		Size: size, Body: io.NopCloser(bytes.NewReader(upload.data)),
	}, nil
}

type fakeLock struct{ released bool }

func (l *fakeLock) Release(context.Context) error { l.released = true; return nil }

type storedIntake struct {
	row       IntakeRow
	revisions int
}

type fakeStore struct {
	mu             sync.Mutex
	locked         bool
	nextRunID      int64
	intakes        map[string]storedIntake
	finished       []Summary
	finishHeaders  []string
	finishMessages []string
	applyErr       error
}

func newFakeStore() *fakeStore { return &fakeStore{intakes: make(map[string]storedIntake)} }

func (s *fakeStore) TrySyncLock(context.Context) (SyncLock, bool, error) {
	if s.locked {
		return nil, false, nil
	}
	return &fakeLock{}, true, nil
}

func (s *fakeStore) StartSyncRun(context.Context, Trigger, string, string) (int64, error) {
	s.nextRunID++
	return s.nextRunID, nil
}

func (s *fakeStore) FinishSyncRun(_ context.Context, summary Summary, headerSHA, message string) error {
	s.finished = append(s.finished, summary)
	s.finishHeaders = append(s.finishHeaders, headerSHA)
	s.finishMessages = append(s.finishMessages, message)
	return nil
}

func (s *fakeStore) ApplyRow(_ context.Context, _ int64, row IntakeRow) (ApplyDisposition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applyErr != nil {
		return ApplyUnchanged, s.applyErr
	}
	stored, exists := s.intakes[row.ExternalKey]
	if !exists {
		s.intakes[row.ExternalKey] = storedIntake{row: row, revisions: 1}
		return ApplyNew, nil
	}
	if stored.row.RawSHA256 == row.RawSHA256 {
		if row.State == "exception" || stored.row.State == "exception" {
			stored.row.State = row.State
			stored.row.ExceptionMessage = row.ExceptionMessage
			s.intakes[row.ExternalKey] = stored
		}
		return ApplyUnchanged, nil
	}
	stored.row = row
	stored.revisions++
	s.intakes[row.ExternalKey] = stored
	return ApplyChanged, nil
}

func TestServiceSyncIsIdempotentAndAppendsChangedRevision(t *testing.T) {
	cfg := testConfig()
	row := validSourceRow()
	source := &fakeSource{data: sheetWithRows(row)}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	first, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.Status != "succeeded" || first.Seen != 1 || first.New != 1 || first.Changed != 0 || first.Errors != 0 {
		t.Fatalf("first summary: %+v", first)
	}
	second, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.New != 0 || second.Changed != 0 || len(store.intakes) != 1 {
		t.Fatalf("idempotent summary=%+v intakes=%d", second, len(store.intakes))
	}

	changed := append([]any(nil), row...)
	changed[4] = "Corrected additional information"
	source.data = sheetWithRows(changed)
	third, err := service.Sync(context.Background(), Trigger{Type: "admin", AdminID: int64Pointer(7)})
	if err != nil {
		t.Fatalf("third sync: %v", err)
	}
	if third.New != 0 || third.Changed != 1 || len(store.intakes) != 1 {
		t.Fatalf("changed summary=%+v intakes=%d", third, len(store.intakes))
	}
	for _, intake := range store.intakes {
		if intake.revisions != 2 {
			t.Fatalf("revision count: %d", intake.revisions)
		}
		if got := intake.row.RawData["Your Name & Role at Club/League"]; got != "Jane Smith, Secretary" {
			t.Fatalf("raw reporter name/role: %v", got)
		}
	}
}

func TestServiceHeaderDriftFailsClosedBeforeRows(t *testing.T) {
	cfg := testConfig()
	headers := append([]string(nil), cfg.Schema.Headers...)
	headers[8] = "Club accused"
	source := &fakeSource{data: SheetData{Values: [][]any{stringsToAny(headers), validSourceRow()}}}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	var mismatch *HeaderMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected HeaderMismatchError, got %v", err)
	}
	if summary.Status != "failed" || summary.Seen != 1 || summary.New != 0 || len(store.intakes) != 0 {
		t.Fatalf("summary=%+v intakes=%d", summary, len(store.intakes))
	}
	if len(store.finished) != 1 || store.finished[0].Status != "failed" {
		t.Fatalf("finished runs: %+v", store.finished)
	}
}

func TestServicePreservesMalformedDateAsException(t *testing.T) {
	cfg := testConfig()
	row := validSourceRow()
	row[10] = "Saturday-ish"
	source := &fakeSource{data: sheetWithRows(row)}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if summary.Status != "partial" || summary.New != 1 || summary.Errors != 1 {
		t.Fatalf("summary: %+v", summary)
	}
	for _, intake := range store.intakes {
		if intake.row.State != "exception" || !strings.Contains(intake.row.ExceptionMessage, "fixture date") {
			t.Fatalf("exception row: %+v", intake.row)
		}
	}
}

func TestServiceSeparatesStableKeyCollisionsIntoExceptions(t *testing.T) {
	cfg := testConfig()
	rowA := validSourceRow()
	rowB := append([]any(nil), rowA...)
	rowB[4] = "A second submission with the same identity fields"
	source := &fakeSource{data: sheetWithRows(rowA, rowB)}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if summary.Seen != 2 || summary.New != 2 || summary.Errors != 2 || len(store.intakes) != 2 {
		t.Fatalf("summary=%+v intakes=%d", summary, len(store.intakes))
	}
	for _, intake := range store.intakes {
		if intake.row.State != "exception" || !strings.Contains(intake.row.ExceptionMessage, "identity collision") {
			t.Fatalf("collision row: %+v", intake.row)
		}
	}
}

func TestServiceMarksEarlierIntakeWhenCollisionAppearsLater(t *testing.T) {
	cfg := testConfig()
	rowA := validSourceRow()
	source := &fakeSource{data: sheetWithRows(rowA)}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil || first.New != 1 {
		t.Fatalf("first sync summary=%+v err=%v", first, err)
	}

	rowB := append([]any(nil), rowA...)
	rowB[4] = "Later duplicate identity"
	source.data = sheetWithRows(rowA, rowB)
	second, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.New != 1 || second.Errors != 2 || len(store.intakes) != 2 {
		t.Fatalf("second summary=%+v intakes=%d", second, len(store.intakes))
	}
	for _, intake := range store.intakes {
		if intake.row.State != "exception" {
			t.Fatalf("existing intake was not marked as a collision: %+v", intake.row)
		}
	}
}

func TestServiceReturnsConflictWhenLockIsHeld(t *testing.T) {
	cfg := testConfig()
	store := newFakeStore()
	store.locked = true
	service, err := NewService(cfg, &fakeSource{}, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sync(context.Background(), Trigger{Type: "n8n"}); !errors.Is(err, ErrSyncInProgress) {
		t.Fatalf("expected ErrSyncInProgress, got %v", err)
	}
}

func TestServiceRecordsStoreFailureAsPartialRun(t *testing.T) {
	cfg := testConfig()
	store := newFakeStore()
	store.applyErr = errors.New("database unavailable")
	service, err := NewService(cfg, &fakeSource{data: sheetWithRows(validSourceRow())}, store)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("expected store error, got %v", err)
	}
	if summary.Status != "partial" || summary.Errors != 1 || len(store.finished) != 1 {
		t.Fatalf("summary=%+v finished=%+v", summary, store.finished)
	}
}

func TestServiceRetainsPermittedDriveUploadBySHA256(t *testing.T) {
	cfg := testConfig()
	cfg.UploadDir = t.TempDir()
	const fileID = "drive-file-1234567890"
	content := []byte("%PDF-1.7\nimmutable evidence\n")
	row := validSourceRow()
	row[12] = "https://drive.google.com/open?id=" + fileID
	source := &fakeSource{
		data: sheetWithRows(row),
		downloads: map[string]fakeUpload{
			fileID: {name: "scorecard.pdf", contentType: "application/pdf", data: content},
		},
		downloadErrors: map[string]error{},
	}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if summary.Status != "succeeded" || summary.New != 1 || summary.Errors != 0 {
		t.Fatalf("summary: %+v", summary)
	}
	for _, intake := range store.intakes {
		if len(intake.row.Attachments) != 1 {
			t.Fatalf("attachments: %+v", intake.row.Attachments)
		}
		attachment := intake.row.Attachments[0]
		if attachment.DriveFileID != fileID || attachment.ContentType != "application/pdf" || attachment.SizeBytes != int64(len(content)) {
			t.Fatalf("attachment metadata: %+v", attachment)
		}
		expectedDigest := sha256.Sum256(content)
		expectedSHA := hex.EncodeToString(expectedDigest[:])
		if attachment.SHA256 != expectedSHA || attachment.StorageKey != "sha256/"+expectedSHA[:2]+"/"+expectedSHA {
			t.Fatalf("content address: sha=%q key=%q", attachment.SHA256, attachment.StorageKey)
		}
		retainedPath := filepath.Join(cfg.UploadDir, filepath.FromSlash(attachment.StorageKey))
		retained, err := os.ReadFile(retainedPath)
		if err != nil {
			t.Fatalf("read retained upload: %v", err)
		}
		if !bytes.Equal(retained, content) {
			t.Fatalf("retained bytes: %q", retained)
		}
		if runtime.GOOS != "windows" {
			info, statErr := os.Stat(retainedPath)
			if statErr != nil {
				t.Fatalf("stat retained upload: %v", statErr)
			}
			if got := info.Mode().Perm(); got != 0400 {
				t.Fatalf("retained upload mode = %#o, want 0400", got)
			}
		}
	}
}

func TestServiceAppendsRevisionWhenDriveBytesChangeAtSameFileID(t *testing.T) {
	cfg := testConfig()
	cfg.UploadDir = t.TempDir()
	const fileID = "drive-file-1234567890"
	row := validSourceRow()
	row[12] = "https://drive.google.com/open?id=" + fileID
	source := &fakeSource{
		data:           sheetWithRows(row),
		downloads:      map[string]fakeUpload{fileID: {name: "scorecard.pdf", contentType: "application/pdf", data: []byte("version one")}},
		downloadErrors: map[string]error{},
	}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil || first.New != 1 {
		t.Fatalf("first sync: summary=%+v err=%v", first, err)
	}
	var firstSHA string
	for _, intake := range store.intakes {
		firstSHA = intake.row.RawSHA256
	}
	source.downloads[fileID] = fakeUpload{name: "scorecard.pdf", contentType: "application/pdf", data: []byte("version two")}
	second, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed != 1 || second.New != 0 {
		t.Fatalf("second sync: %+v", second)
	}
	for _, intake := range store.intakes {
		if intake.revisions != 2 || intake.row.RawSHA256 == firstSHA {
			t.Fatalf("changed upload did not append revision: %+v", intake)
		}
	}
}

func TestServiceMarksInaccessibleDriveUploadAsException(t *testing.T) {
	cfg := testConfig()
	cfg.UploadDir = t.TempDir()
	const fileID = "drive-file-1234567890"
	row := validSourceRow()
	row[12] = "https://drive.google.com/file/d/" + fileID + "/view"
	source := &fakeSource{
		data: sheetWithRows(row), downloads: map[string]fakeUpload{},
		downloadErrors: map[string]error{fileID: errors.New("Google Drive HTTP 403")},
	}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if summary.Status != "partial" || summary.New != 1 || summary.Errors != 1 {
		t.Fatalf("summary: %+v", summary)
	}
	for _, intake := range store.intakes {
		if intake.row.State != "exception" || !strings.Contains(intake.row.ExceptionMessage, "403") || len(intake.row.Attachments) != 0 {
			t.Fatalf("exception intake: %+v", intake.row)
		}
	}
}

func TestServiceRejectsDisallowedDriveContentType(t *testing.T) {
	cfg := testConfig()
	cfg.UploadDir = t.TempDir()
	const fileID = "drive-file-1234567890"
	row := validSourceRow()
	row[12] = fileID
	source := &fakeSource{
		data: sheetWithRows(row),
		downloads: map[string]fakeUpload{
			fileID: {name: "payload.exe", contentType: "application/x-msdownload", data: []byte("MZ")},
		},
		downloadErrors: map[string]error{},
	}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if summary.Status != "partial" || summary.Errors != 1 {
		t.Fatalf("summary: %+v", summary)
	}
	for _, intake := range store.intakes {
		if !strings.Contains(intake.row.ExceptionMessage, "not permitted") {
			t.Fatalf("exception: %q", intake.row.ExceptionMessage)
		}
	}
}

func TestServiceAcceptsSevenDriveUploadsWithinDefaultLimit(t *testing.T) {
	cfg := testConfig()
	cfg.UploadDir = t.TempDir()
	row := validSourceRow()
	references := make([]string, 0, 7)
	downloads := make(map[string]fakeUpload, 7)
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("drive-file-%010d", i)
		references = append(references, id)
		downloads[id] = fakeUpload{
			name: fmt.Sprintf("evidence-%d.jpg", i), contentType: "image/jpeg", data: []byte("image"),
		}
	}
	row[12] = strings.Join(references, ", ")
	source := &fakeSource{data: sheetWithRows(row), downloads: downloads, downloadErrors: map[string]error{}}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if summary.New != 1 || summary.Errors != 0 {
		t.Fatalf("summary: %+v", summary)
	}
	for _, intake := range store.intakes {
		if intake.row.State == "exception" || len(intake.row.Attachments) != 7 {
			t.Fatalf("seven-file report was not imported cleanly: %+v", intake.row)
		}
	}
}

func TestServiceRejectsUploadCountBeforeDriveRequests(t *testing.T) {
	cfg := testConfig()
	cfg.UploadDir = t.TempDir()
	cfg.UploadMaxFiles = 1
	row := validSourceRow()
	row[12] = "drive-file-1234567890, drive-file-0987654321"
	source := &fakeSource{data: sheetWithRows(row), downloads: map[string]fakeUpload{}, downloadErrors: map[string]error{}}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if summary.Errors != 1 || len(source.downloadCalls) != 0 {
		t.Fatalf("summary=%+v download calls=%v", summary, source.downloadCalls)
	}
}

func TestParseUploadReferencesRejectsNonDriveHost(t *testing.T) {
	if _, err := parseUploadReferences("https://evil.example/file/d/drive-file-1234567890/view"); err == nil {
		t.Fatal("expected non-Google Drive host to be rejected")
	}
}

func TestParseUploadReferencesAllowsPlayCricketEvidenceLinksWithoutDownloading(t *testing.T) {
	for _, reference := range []string{
		"https://play-cricket.com/website/results/12345",
		"https://springhead.play-cricket.com/website/results/12345",
	} {
		references, err := parseUploadReferences(reference)
		if err != nil {
			t.Fatalf("parse %q: %v", reference, err)
		}
		if len(references) != 0 {
			t.Fatalf("expected %q to remain an external reference, got %+v", reference, references)
		}
	}
}

func TestParseUploadReferencesRejectsUnsafePlayCricketLookalikes(t *testing.T) {
	for _, reference := range []string{
		"http://springhead.play-cricket.com/website/results/12345",
		"https://play-cricket.com.evil.example/website/results/12345",
		"https://evilplay-cricket.com/website/results/12345",
		"https://user@springhead.play-cricket.com/website/results/12345",
	} {
		if _, err := parseUploadReferences(reference); err == nil {
			t.Fatalf("expected %q to be rejected", reference)
		}
	}
}

func TestServiceRetainsPlayCricketEvidenceLinkWithoutDriveRequest(t *testing.T) {
	cfg := testConfig()
	cfg.UploadDir = t.TempDir()
	row := validSourceRow()
	const evidenceURL = "https://springhead.play-cricket.com/website/results/12345"
	row[12] = evidenceURL
	source := &fakeSource{data: sheetWithRows(row), downloads: map[string]fakeUpload{}, downloadErrors: map[string]error{}}
	store := newFakeStore()
	service, err := NewService(cfg, source, store)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := service.Sync(context.Background(), Trigger{Type: "n8n"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if summary.Errors != 0 || len(source.downloadCalls) != 0 {
		t.Fatalf("summary=%+v download calls=%v", summary, source.downloadCalls)
	}
	for _, intake := range store.intakes {
		if intake.row.UploadCell != evidenceURL {
			t.Fatalf("stored upload cell = %q, want %q", intake.row.UploadCell, evidenceURL)
		}
	}
}

func sheetWithRows(rows ...[]any) SheetData {
	values := make([][]any, 0, len(rows)+1)
	values = append(values, stringsToAny(DefaultGoogleFormSchema().Headers))
	values = append(values, rows...)
	return SheetData{Range: "'Form responses 1'!A1:N", Values: values}
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func int64Pointer(value int64) *int64 { return &value }
