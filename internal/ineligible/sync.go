package ineligible

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cricket-ground-feedback/internal/db"
)

var (
	ErrImportDisabled = errors.New("ineligible-player import is disabled")
	ErrSyncInProgress = errors.New("an ineligible-player sync is already running")
)

const (
	googleFormOrigin         = "google_form"
	googleFormImportLookback = 24 * time.Hour
)

// Source is the read-only form values provider used by Service.
type Source interface {
	Fetch(context.Context) (SheetData, error)
}

// SyncLock owns the session-level advisory lock for a single run.
type SyncLock interface {
	Release(context.Context) error
}

// Store persists append-only sync observations and intake revisions.
type Store interface {
	TrySyncLock(context.Context) (SyncLock, bool, error)
	StartSyncRun(context.Context, Trigger, string, string) (int64, error)
	FinishSyncRun(context.Context, Summary, string, string) error
	ApplyRow(context.Context, int64, IntakeRow) (ApplyDisposition, error)
}

type ApplyDisposition int

const (
	ApplyUnchanged ApplyDisposition = iota
	ApplyNew
	ApplyChanged
	ApplyException
)

// Trigger records whether n8n, an administrator, or another system action
// initiated the same reusable sync service.
type Trigger struct {
	Type    string
	AdminID *int64
}

// Summary is returned by both the internal endpoint and admin callers.
type Summary struct {
	RunID   int64  `json:"run_id"`
	Status  string `json:"status"`
	Seen    int    `json:"seen"`
	New     int    `json:"new"`
	Changed int    `json:"changed"`
	Errors  int    `json:"errors"`
}

// IntakeRow is a normalized queue projection plus an immutable raw snapshot.
type IntakeRow struct {
	ExternalKey       string
	SourceReference   string
	ExternalCreatedAt *time.Time
	State             string
	ReportingClubText string
	OffendingClubText string
	TeamText          string
	PlayerText        string
	FixtureDate       *time.Time
	ExceptionMessage  string
	SourceRowNumber   int
	RawData           map[string]any
	RawSHA256         string
	HeaderSHA256      string
	UploadCell        string
	Attachments       []StoredAttachment
}

// HeaderMismatchError identifies schema drift without accepting or guessing at
// a renamed column.
type HeaderMismatchError struct {
	Column   int
	Expected string
	Observed string
	Count    int
}

func (e *HeaderMismatchError) Error() string {
	if e.Count != GoogleFormsHeaderCount {
		return fmt.Sprintf("Google Form header count changed: expected %d, got %d", GoogleFormsHeaderCount, e.Count)
	}
	return fmt.Sprintf("Google Form header %d changed: expected %q, got %q", e.Column+1, e.Expected, e.Observed)
}

// Service runs the exact same import for scheduled and manual/admin callers.
type Service struct {
	cfg    Config
	source Source
	store  Store
	loc    *time.Location
	now    func() time.Time
}

func NewService(cfg Config, source Source, store Store) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if source == nil || store == nil {
		return nil, fmt.Errorf("ineligible-player source and store are required")
	}
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		loc = time.UTC
	}
	return &Service{cfg: cfg, source: source, store: store, loc: loc, now: time.Now}, nil
}

// SyncFromEnv is the admin-callable production entry point. It intentionally
// shares all locking, validation, and persistence behavior with the n8n route.
func SyncFromEnv(ctx context.Context, pool *db.Pool, trigger Trigger) (Summary, error) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		return Summary{}, err
	}
	if !cfg.Enabled {
		return Summary{}, ErrImportDisabled
	}
	store := NewPGStore(pool)
	trigger = normalizeTrigger(trigger)
	if _, err = store.authorizeRollout(ctx, trigger, cfg.BootstrapEnabled); err != nil {
		status := "blocked"
		if errors.Is(err, ErrGoogleImportRetired) {
			status = "retired"
		}
		return Summary{Status: status}, err
	}
	source, sourceErr := NewGoogleSheetsSource(cfg, nil)
	var sourceProvider Source = source
	if sourceErr != nil {
		// Credential decoding/auth setup failures should still become durable
		// failed runs instead of disappearing before operational health checks.
		sourceProvider = failedSource{err: sourceErr}
	}
	service, err := NewService(cfg, sourceProvider, store)
	if err != nil {
		return Summary{}, err
	}
	return service.Sync(ctx, trigger)
}

type failedSource struct{ err error }

func (s failedSource) Fetch(context.Context) (SheetData, error) { return SheetData{}, s.err }

// Sync takes a non-blocking advisory lock, records the run, validates the
// complete header before writes, and then appends only new or changed rows.
func (s *Service) Sync(ctx context.Context, trigger Trigger) (summary Summary, resultErr error) {
	if !s.cfg.Enabled {
		return Summary{}, ErrImportDisabled
	}
	lock, acquired, err := s.store.TrySyncLock(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("acquire ineligible-player sync lock: %w", err)
	}
	if !acquired {
		return Summary{}, ErrSyncInProgress
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if releaseErr := lock.Release(releaseCtx); releaseErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("release ineligible-player sync lock: %w", releaseErr)
		}
	}()

	sourceReference := sourceReference(s.cfg)
	expectedHeaderSHA := headerSHA256(s.cfg.Schema.Headers)
	runID, err := s.store.StartSyncRun(ctx, normalizeTrigger(trigger), sourceReference, expectedHeaderSHA)
	if err != nil {
		return Summary{}, fmt.Errorf("start ineligible-player sync run: %w", err)
	}
	summary = Summary{RunID: runID, Status: "running"}

	data, err := s.source.Fetch(ctx)
	if err != nil {
		summary.Status = "failed"
		return summary, s.finishFailure(ctx, summary, "", fmt.Errorf("fetch protected Google Form: %w", err))
	}
	if len(data.Values) > 0 {
		summary.Seen = len(data.Values) - 1
	}
	observedHeaders := observedHeader(data.Values)
	observedHeaderSHA := headerSHA256(observedHeaders)
	if err := validateHeader(s.cfg.Schema.Headers, observedHeaders); err != nil {
		summary.Status = "failed"
		return summary, s.finishFailure(ctx, summary, observedHeaderSHA, err)
	}

	rows := s.prepareRows(data.Values[1:], sourceReference, expectedHeaderSHA)
	rows = rowsReceivedSince(rows, s.now().Add(-googleFormImportLookback))
	summary.Seen = len(rows)
	storeFailures := make([]error, 0)
	for index := range rows {
		s.attachUploads(ctx, &rows[index])
		finalizeAttachmentRevisionSHA(&rows[index])
		row := rows[index]
		rowErrored := row.State == "exception"
		if rowErrored {
			summary.Errors++
		}
		disposition, applyErr := s.store.ApplyRow(ctx, runID, row)
		if applyErr != nil {
			if !rowErrored {
				summary.Errors++
			}
			if len(storeFailures) < 5 {
				storeFailures = append(storeFailures, fmt.Errorf("source row %d: %w", row.SourceRowNumber, applyErr))
			}
			continue
		}
		switch disposition {
		case ApplyNew:
			summary.New++
		case ApplyChanged:
			summary.Changed++
		case ApplyException:
			if !rowErrored {
				summary.Errors++
			}
		}
	}
	if summary.Errors > 0 {
		summary.Status = "partial"
	} else {
		summary.Status = "succeeded"
	}
	if len(storeFailures) > 0 {
		storeErr := errors.Join(storeFailures...)
		finishCtx, finishCancel := syncCompletionContext()
		defer finishCancel()
		if finishErr := s.store.FinishSyncRun(finishCtx, summary, expectedHeaderSHA, truncateMessage(storeErr.Error(), 2000)); finishErr != nil {
			storeErr = errors.Join(storeErr, fmt.Errorf("finish sync run: %w", finishErr))
		}
		return summary, storeErr
	}
	finishCtx, finishCancel := syncCompletionContext()
	defer finishCancel()
	if err := s.store.FinishSyncRun(finishCtx, summary, expectedHeaderSHA, ""); err != nil {
		return summary, fmt.Errorf("finish ineligible-player sync run: %w", err)
	}
	return summary, nil
}

func rowsReceivedSince(rows []IntakeRow, cutoff time.Time) []IntakeRow {
	recent := make([]IntakeRow, 0, len(rows))
	for _, row := range rows {
		// A malformed Form timestamp is retained for manual attention instead of
		// being silently discarded. Valid timestamps older than the daily window
		// have already been observed by an earlier run and require no work here.
		if row.ExternalCreatedAt != nil && row.ExternalCreatedAt.Before(cutoff) {
			continue
		}
		recent = append(recent, row)
	}
	return recent
}

func (s *Service) finishFailure(_ context.Context, summary Summary, headerSHA string, cause error) error {
	finishCtx, finishCancel := syncCompletionContext()
	defer finishCancel()
	finishErr := s.store.FinishSyncRun(finishCtx, summary, headerSHA, truncateMessage(cause.Error(), 2000))
	if finishErr != nil {
		return errors.Join(cause, fmt.Errorf("finish failed sync run: %w", finishErr))
	}
	return cause
}

func syncCompletionContext() (context.Context, context.CancelFunc) {
	// A caller timeout must not strand an otherwise durable run in "running".
	// Completion remains tightly bounded and does not continue row processing.
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func normalizeTrigger(trigger Trigger) Trigger {
	trigger.Type = strings.TrimSpace(trigger.Type)
	switch trigger.Type {
	case "admin":
		if trigger.AdminID == nil {
			trigger.Type = "system"
		}
	case "n8n", "system":
		trigger.AdminID = nil
	default:
		trigger.Type = "system"
		trigger.AdminID = nil
	}
	return trigger
}

func validateHeader(expected, observed []string) error {
	if len(observed) != len(expected) {
		return &HeaderMismatchError{Count: len(observed)}
	}
	for index := range expected {
		if observed[index] != expected[index] {
			return &HeaderMismatchError{
				Column: index, Expected: expected[index], Observed: observed[index], Count: len(observed),
			}
		}
	}
	return nil
}

func observedHeader(values [][]any) []string {
	if len(values) == 0 {
		return nil
	}
	headers := make([]string, len(values[0]))
	for i, value := range values[0] {
		headers[i] = cellString(value)
	}
	return headers
}

func headerSHA256(headers []string) string {
	raw, _ := json.Marshal(headers)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func sourceReference(cfg Config) string {
	return fmt.Sprintf("google-sheets://%s/%s#%s", cfg.SpreadsheetID, cfg.SheetGID, cfg.SheetRange)
}

type preparedRow struct {
	row     IntakeRow
	baseKey string
}

func (s *Service) prepareRows(sourceRows [][]any, sourceRef, headerSHA string) []IntakeRow {
	prepared := make([]preparedRow, 0, len(sourceRows))
	counts := make(map[string]int, len(sourceRows))
	for index, sourceRow := range sourceRows {
		row := s.prepareRow(index+2, sourceRow, sourceRef, headerSHA)
		prepared = append(prepared, preparedRow{row: row, baseKey: row.ExternalKey})
		counts[row.ExternalKey]++
	}
	rows := make([]IntakeRow, 0, len(prepared))
	collisionOccurrence := make(map[string]int)
	for _, item := range prepared {
		row := item.row
		if counts[item.baseKey] > 1 {
			// Keep the earliest row on its original stable key. If a later
			// submission introduces a collision, this avoids changing the key
			// that may already have been imported by an earlier run. Only the
			// additional indistinguishable response uses row provenance as a
			// deterministic disambiguator.
			if collisionOccurrence[item.baseKey] > 0 {
				row.ExternalKey = sha256Hex(item.baseKey + "|collision-source-row|" + strconv.Itoa(row.SourceRowNumber))
			}
			collisionOccurrence[item.baseKey]++
			row.State = "exception"
			row.ExceptionMessage = appendException(row.ExceptionMessage, "identity collision: multiple source rows share the stable response key")
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *Service) prepareRow(rowNumber int, sourceRow []any, sourceRef, headerSHA string) IntakeRow {
	values := make([]any, GoogleFormsHeaderCount)
	for i := range values {
		values[i] = ""
	}
	copy(values, sourceRow)
	rawData := make(map[string]any, GoogleFormsHeaderCount+1)
	for index, header := range s.cfg.Schema.Headers {
		rawData[header] = values[index]
	}
	validationErrors := make([]string, 0)
	if len(sourceRow) > GoogleFormsHeaderCount {
		rawData["_extra_columns"] = append([]any(nil), sourceRow[GoogleFormsHeaderCount:]...)
		validationErrors = append(validationErrors, fmt.Sprintf("row has %d columns; expected at most %d", len(sourceRow), GoogleFormsHeaderCount))
	}
	column := func(name string) string { return cellString(values[s.cfg.Schema.Columns[name]]) }
	timestampRaw := strings.TrimSpace(column(ColumnTimestamp))
	reporterEmail := strings.ToLower(strings.TrimSpace(column(ColumnReporterEmail)))
	reportingClub := strings.TrimSpace(column(ColumnReportingClub))
	offendingClub := strings.TrimSpace(column(ColumnOffendingClub))
	team := strings.TrimSpace(column(ColumnTeam))
	player := strings.TrimSpace(column(ColumnPlayer))
	fixtureRaw := strings.TrimSpace(column(ColumnFixtureDate))
	uploadCell := strings.TrimSpace(column(ColumnFileUpload))

	createdAt, timestampErr := parseTimestamp(timestampRaw, s.loc)
	if timestampErr != nil {
		validationErrors = append(validationErrors, timestampErr.Error())
	}
	fixtureDate, fixtureErr := parseFixtureDate(fixtureRaw, s.loc)
	if fixtureErr != nil {
		validationErrors = append(validationErrors, fixtureErr.Error())
	}
	for _, required := range []struct {
		label string
		value string
	}{
		{label: "reporter email", value: reporterEmail},
		{label: "reporting club", value: reportingClub},
		{label: "offending club", value: offendingClub},
		{label: "team", value: team},
		{label: "player", value: player},
	} {
		if required.value == "" {
			validationErrors = append(validationErrors, required.label+" is blank")
		}
	}

	identityTimestamp := normalizeIdentity(timestampRaw)
	if createdAt != nil {
		identityTimestamp = createdAt.UTC().Format(time.RFC3339Nano)
	}
	identityFixture := normalizeIdentity(fixtureRaw)
	if fixtureDate != nil {
		identityFixture = fixtureDate.Format("2006-01-02")
	}
	baseKey := sha256Hex(strings.Join([]string{
		"google-form-v1", s.cfg.SpreadsheetID, s.cfg.SheetGID,
		identityTimestamp, reporterEmail, normalizeIdentity(player),
		normalizeIdentity(offendingClub), normalizeIdentity(team), identityFixture,
	}, "|"))
	canonicalRaw, _ := json.Marshal(struct {
		Headers []string `json:"headers"`
		Values  []any    `json:"values"`
		Extra   []any    `json:"extra,omitempty"`
	}{Headers: s.cfg.Schema.Headers, Values: values, Extra: extraValues(sourceRow)})

	state := "new"
	exceptionMessage := ""
	if len(validationErrors) > 0 {
		state = "exception"
		exceptionMessage = strings.Join(validationErrors, "; ")
	}
	return IntakeRow{
		ExternalKey: baseKey, SourceReference: sourceRef,
		ExternalCreatedAt: createdAt, State: state,
		ReportingClubText: reportingClub, OffendingClubText: offendingClub,
		TeamText: team, PlayerText: player, FixtureDate: fixtureDate,
		ExceptionMessage: truncateMessage(exceptionMessage, 2000), SourceRowNumber: rowNumber,
		RawData: rawData, RawSHA256: sha256Hex(string(canonicalRaw)), HeaderSHA256: headerSHA,
		UploadCell: uploadCell,
	}
}

func (s *Service) attachUploads(ctx context.Context, row *IntakeRow) {
	references, err := parseUploadReferences(row.UploadCell)
	if err != nil {
		markRowException(row, err.Error())
		return
	}
	if len(references) == 0 {
		return
	}
	if len(references) > s.cfg.UploadMaxFiles {
		markRowException(row, fmt.Sprintf("File Upload contains %d files; configured maximum is %d", len(references), s.cfg.UploadMaxFiles))
		return
	}
	downloader, ok := s.source.(UploadDownloader)
	if !ok {
		markRowException(row, "Google Drive upload retrieval is not configured")
		return
	}
	var totalBytes int64
	for _, reference := range references {
		remaining := s.cfg.UploadMaxTotalBytes - totalBytes
		if remaining < 1 {
			markRowException(row, "File Upload exceeds the configured total byte limit")
			break
		}
		fileLimit := s.cfg.UploadMaxFileBytes
		if remaining < fileLimit {
			fileLimit = remaining
		}
		download, downloadErr := downloader.DownloadUpload(ctx, reference.FileID, fileLimit)
		if downloadErr != nil {
			markRowException(row, truncateMessage(downloadErr.Error(), 500))
			continue
		}
		stored, storeErr := storeUpload(ctx, s.cfg, reference, download, fileLimit)
		if storeErr != nil {
			markRowException(row, truncateMessage(storeErr.Error(), 500))
			continue
		}
		totalBytes += stored.SizeBytes
		row.Attachments = append(row.Attachments, stored)
	}
}

// The sheet row itself contains only stable Drive URLs. Bind the ordered,
// downloaded upload manifest into the revision digest so replacing bytes at
// the same Drive file ID appends a new immutable intake revision.
func finalizeAttachmentRevisionSHA(row *IntakeRow) {
	if strings.TrimSpace(row.UploadCell) == "" {
		return
	}
	type revisionAttachment struct {
		DriveFileID  string `json:"drive_file_id"`
		OriginalName string `json:"original_name"`
		ContentType  string `json:"content_type"`
		SizeBytes    int64  `json:"size_bytes"`
		SHA256       string `json:"sha256"`
	}
	manifest := make([]revisionAttachment, 0, len(row.Attachments))
	for _, attachment := range row.Attachments {
		manifest = append(manifest, revisionAttachment{
			DriveFileID: attachment.DriveFileID, OriginalName: attachment.OriginalName,
			ContentType: attachment.ContentType, SizeBytes: attachment.SizeBytes, SHA256: attachment.SHA256,
		})
	}
	encoded, _ := json.Marshal(struct {
		RowSHA      string               `json:"row_sha256"`
		Attachments []revisionAttachment `json:"attachments"`
	}{RowSHA: row.RawSHA256, Attachments: manifest})
	row.RawSHA256 = sha256Hex(string(encoded))
}

func markRowException(row *IntakeRow, message string) {
	row.State = "exception"
	message = strings.ReplaceAll(message, "\x00", "")
	row.ExceptionMessage = truncateMessage(appendException(row.ExceptionMessage, message), 2000)
}

func extraValues(row []any) []any {
	if len(row) <= GoogleFormsHeaderCount {
		return nil
	}
	return row[GoogleFormsHeaderCount:]
}

func parseTimestamp(value string, loc *time.Location) (*time.Time, error) {
	if value == "" {
		return nil, fmt.Errorf("timestamp is blank")
	}
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"02/01/2006 15:04:05", "2/1/2006 15:04:05",
		"02/01/2006 15:04", "2/1/2006 15:04",
		"2006-01-02 15:04:05", "2006-01-02 15:04",
	} {
		parsed, err := time.ParseInLocation(layout, value, loc)
		if err == nil {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("timestamp %q is malformed", value)
}

func parseFixtureDate(value string, loc *time.Location) (*time.Time, error) {
	if value == "" {
		return nil, fmt.Errorf("fixture date is blank")
	}
	for _, layout := range []string{
		"02/01/2006", "2/1/2006", "2006-01-02", "2 January 2006", "02 January 2006",
	} {
		parsed, err := time.ParseInLocation(layout, value, loc)
		if err == nil {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("fixture date %q is malformed", value)
}

func cellString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func normalizeIdentity(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func appendException(existing, message string) string {
	if existing == "" {
		return message
	}
	return existing + "; " + message
}

func truncateMessage(value string, max int) string {
	if len(value) <= max {
		return value
	}
	cut := max
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}
