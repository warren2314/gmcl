package junior

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type FormField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}
type FormInput struct {
	ID, CurrentVersionID, SeasonID int64
	Title, Description             string
	DueDate                        time.Time
	RequiresReview                 bool
	Fields                         []FormField
	ClubIDs                        []int32
}
type FormSummary struct {
	ID, VersionID, SeasonID                         int64
	Title, Season                                   string
	Revision                                        int
	DueDate                                         time.Time
	Outstanding, Overdue, RequiresReview, Completed int64
}
type FormAssignment struct {
	ID, FormID, VersionID, SeasonID  int64
	ClubID                           int32
	Title, Club, Description, Status string
	Revision, DraftRevision          int
	DueDate                          time.Time
	RequiresReview                   bool
	Fields                           []FormField
	Values                           map[string]string
	SubmittedValues                  map[string]string
	SubmissionID                     int64
	SubmittedAt                      *time.Time
	ReviewReason                     string
}
type FormSubmission struct {
	ID          int64
	Revision    int
	Values      map[string]string
	SubmittedAt time.Time
}

func FormAssignmentStatus(a FormAssignment, now time.Time) string {
	switch a.Status {
	case "submitted":
		return "Requires review"
	case "completed":
		return "Completed"
	case "superseded":
		return "Superseded"
	}
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		london = time.UTC
	}
	if a.DueDate.Format("2006-01-02") < now.In(london).Format("2006-01-02") {
		return "Overdue"
	}
	if a.Status == "returned" {
		return "Changes requested"
	}
	return "Outstanding"
}

func (s *Service) FormSubmissionHistory(ctx context.Context, p Principal, assignmentID int64) ([]FormSubmission, error) {
	if _, err := s.FormAssignment(ctx, p, assignmentID); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, "SELECT id,revision,values,submitted_at FROM junior_form_submissions WHERE assignment_id=$1 ORDER BY revision DESC", assignmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []FormSubmission{}
	for rows.Next() {
		var item FormSubmission
		var values []byte
		if err = rows.Scan(&item.ID, &item.Revision, &values, &item.SubmittedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(values, &item.Values); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

var formKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

func ValidateFormFields(fields []FormField) error {
	if len(fields) < 1 || len(fields) > 10 {
		return fmt.Errorf("%w: add between one and ten fields", ErrValidation)
	}
	keys := map[string]bool{}
	teamFields := 0
	for _, f := range fields {
		if !formKey.MatchString(f.Key) || keys[f.Key] || strings.TrimSpace(f.Label) == "" || len(f.Label) > 160 || strings.ContainsAny(f.Label, "\x00\r\n") {
			return fmt.Errorf("%w: each field needs a unique key and a short label", ErrValidation)
		}
		keys[f.Key] = true
		switch f.Type {
		case "team_entries":
			teamFields++
			if teamFields > 1 || !f.Required {
				return fixtureValidation("use one required Team entries question per form")
			}
		case "text", "long_text", "email", "number", "date", "yes_no":
		case "select":
			if len(f.Options) < 1 || len(f.Options) > 30 {
				return fmt.Errorf("%w: choice fields need one to thirty choices", ErrValidation)
			}
			options := map[string]bool{}
			for _, option := range f.Options {
				if strings.TrimSpace(option) == "" || len(option) > 160 || options[option] || strings.ContainsRune(option, 0) {
					return fmt.Errorf("%w: choices must be distinct and nonempty", ErrValidation)
				}
				options[option] = true
			}
		default:
			return fmt.Errorf("%w: unsupported field type", ErrValidation)
		}
	}
	return nil
}

func ValidateFormValues(fields []FormField, values map[string]string, complete bool) error {
	allowed := map[string]bool{}
	for _, f := range fields {
		allowed[f.Key] = true
		value := strings.TrimSpace(values[f.Key])
		if len(values[f.Key]) > 4000 || strings.ContainsRune(values[f.Key], 0) {
			return fmt.Errorf("%w: %s is too long or contains an invalid character", ErrValidation, f.Label)
		}
		if value == "" {
			if complete && f.Required {
				return fmt.Errorf("%w: complete %s", ErrValidation, f.Label)
			}
			continue
		}
		valid := true
		switch f.Type {
		case "team_entries":
			if _, err := ParseTeamEntries(value, complete); err != nil {
				return err
			}
		case "email":
			_, err := CanonicalEmail(value)
			valid = err == nil
		case "number":
			number, err := strconv.ParseFloat(value, 64)
			valid = err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
		case "date":
			_, err := time.Parse("2006-01-02", value)
			valid = err == nil
		case "yes_no":
			valid = value == "yes" || value == "no"
		case "select":
			valid = false
			for _, option := range f.Options {
				if value == option {
					valid = true
				}
			}
		}
		if !valid {
			return fmt.Errorf("%w: check %s", ErrValidation, f.Label)
		}
	}
	for key := range values {
		if !allowed[key] {
			return fmt.Errorf("%w: a response field is not in this published form", ErrValidation)
		}
	}
	return nil
}

func (s *Service) PublishForm(ctx context.Context, p Principal, input FormInput) (int64, error) {
	if HasTeamEntries(input.Fields) {
		input.RequiresReview = true
	}
	if p.AdminID < 1 || p.ReadOnly {
		return 0, ErrForbidden
	}
	if err := ValidateFormFields(input.Fields); err != nil {
		return 0, err
	}
	if input.SeasonID < 1 || strings.TrimSpace(input.Title) == "" || len(input.Title) > 200 || len(input.Description) > 4000 || strings.ContainsAny(input.Title, "\x00\r\n") || strings.ContainsRune(input.Description, 0) || input.DueDate.IsZero() || len(input.ClubIDs) < 1 || len(input.ClubIDs) > 1000 {
		return 0, fmt.Errorf("%w: provide a season, title, deadline and target clubs", ErrValidation)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, 0, true); err != nil {
		return 0, err
	}
	var seasonOpen bool
	if err = tx.QueryRow(ctx, "SELECT state='open' FROM junior_seasons WHERE id=$1 FOR SHARE", input.SeasonID).Scan(&seasonOpen); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if !seasonOpen {
		return 0, fmt.Errorf("%w: this junior season is closed", ErrValidation)
	}
	id, revision := input.ID, 1
	if id == 0 {
		err = tx.QueryRow(ctx, "INSERT INTO junior_forms(season_id,title,created_by_admin_id) VALUES($1,$2,$3) RETURNING id", input.SeasonID, strings.TrimSpace(input.Title), p.AdminID).Scan(&id)
	} else {
		var current, season int64
		err = tx.QueryRow(ctx, `SELECT COALESCE(current_version_id,0),season_id FROM junior_forms WHERE id=$1 FOR UPDATE`, id).Scan(&current, &season)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, ErrNotFound
			}
			return 0, err
		}
		if current != input.CurrentVersionID || season != input.SeasonID {
			return 0, ErrConflict
		}
		err = tx.QueryRow(ctx, "SELECT COALESCE(MAX(revision),0)+1 FROM junior_form_versions WHERE form_id=$1", id).Scan(&revision)
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE junior_forms SET title=$2 WHERE id=$1", id, strings.TrimSpace(input.Title))
		}
	}
	if err != nil {
		return 0, err
	}
	fields, err := json.Marshal(input.Fields)
	if err != nil {
		return 0, err
	}
	var versionID int64
	err = tx.QueryRow(ctx, `INSERT INTO junior_form_versions(form_id,revision,title,description,fields,due_date,requires_review,published_by_admin_id)
  VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,$8) RETURNING id`, id, revision, strings.TrimSpace(input.Title), input.Description, string(fields), input.DueDate.Format("2006-01-02"), input.RequiresReview, p.AdminID).Scan(&versionID)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, "UPDATE junior_forms SET current_version_id=$2 WHERE id=$1", id, versionID)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, "UPDATE junior_form_assignments SET status='superseded' WHERE form_id=$1 AND status IN('assigned','returned')", id)
	if err != nil {
		return 0, err
	}
	unique := map[int32]bool{}
	for _, clubID := range input.ClubIDs {
		if unique[clubID] {
			continue
		}
		unique[clubID] = true
		var active bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM junior_club_seasons WHERE club_id=$1 AND season_id=$2 AND state='active')", clubID, input.SeasonID).Scan(&active); err != nil {
			return 0, err
		}
		if !active {
			return 0, fmt.Errorf("%w: every target club must have active junior participation in this season", ErrValidation)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO junior_form_assignments(form_id,version_id,club_id) VALUES($1,$2,$3)", id, versionID, clubID); err != nil {
			return 0, err
		}
	}
	if err = Audit(ctx, tx, p, 0, "form", id, "form_published", map[string]any{"version_id": versionID, "revision": revision, "clubs": input.ClubIDs}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Service) ListForms(ctx context.Context, p Principal, seasonID int64) ([]FormSummary, error) {
	clubID := int32(0)
	if p.AdminID == 0 || p.ReadOnly {
		clubID = p.ClubID
	}
	if err := Authorize(ctx, s.DB, p, clubID, false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT f.id,v.id,f.season_id,f.title,s.name,v.revision,v.due_date,
 COUNT(a.id) FILTER(WHERE a.status IN('assigned','returned')),
 COUNT(a.id) FILTER(WHERE a.status IN('assigned','returned') AND v2.due_date<(now() AT TIME ZONE 'Europe/London')::date),
 COUNT(a.id) FILTER(WHERE a.status='submitted'),COUNT(a.id) FILTER(WHERE a.status='completed')
 FROM junior_forms f JOIN junior_seasons s ON s.id=f.season_id JOIN junior_form_versions v ON v.id=f.current_version_id
 LEFT JOIN junior_form_assignments a ON a.form_id=f.id AND ($1::integer=0 OR a.club_id=$1)
 LEFT JOIN junior_form_versions v2 ON v2.id=a.version_id
 WHERE ($2::bigint=0 OR f.season_id=$2) AND ($1::integer=0 OR a.id IS NOT NULL)
 GROUP BY f.id,v.id,s.name ORDER BY v.published_at DESC,f.id DESC`, clubID, seasonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []FormSummary{}
	for rows.Next() {
		var f FormSummary
		if err = rows.Scan(&f.ID, &f.VersionID, &f.SeasonID, &f.Title, &f.Season, &f.Revision, &f.DueDate, &f.Outstanding, &f.Overdue, &f.RequiresReview, &f.Completed); err != nil {
			return nil, err
		}
		items = append(items, f)
	}
	return items, rows.Err()
}

func (s *Service) FormAssignments(ctx context.Context, p Principal, formID int64) ([]FormAssignment, error) {
	clubID := int32(0)
	if p.AdminID == 0 || p.ReadOnly {
		clubID = p.ClubID
	}
	if err := Authorize(ctx, s.DB, p, clubID, false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT a.id,a.form_id,a.version_id,f.season_id,a.club_id,v.title,c.name,v.description,a.status,
 v.revision,COALESCE(d.revision,0),v.due_date,v.requires_review,v.fields,COALESCE(d.values,'{}'::jsonb),
 COALESCE(a.latest_submission_id,0),sub.submitted_at,COALESCE((SELECT reason FROM junior_form_reviews r WHERE r.assignment_id=a.id ORDER BY id DESC LIMIT 1),''),COALESCE(sub.values,'{}'::jsonb)
 FROM junior_form_assignments a JOIN junior_forms f ON f.id=a.form_id JOIN junior_form_versions v ON v.id=a.version_id
 JOIN clubs c ON c.id=a.club_id LEFT JOIN junior_form_drafts d ON d.assignment_id=a.id LEFT JOIN junior_form_submissions sub ON sub.id=a.latest_submission_id
 WHERE ($1::bigint=0 OR a.form_id=$1) AND ($2::integer=0 OR a.club_id=$2)
 ORDER BY a.form_id DESC,v.revision DESC,c.name,a.id`, formID, clubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []FormAssignment{}
	for rows.Next() {
		var a FormAssignment
		var fields, values, submitted []byte
		if err = rows.Scan(&a.ID, &a.FormID, &a.VersionID, &a.SeasonID, &a.ClubID, &a.Title, &a.Club, &a.Description, &a.Status, &a.Revision, &a.DraftRevision, &a.DueDate, &a.RequiresReview, &fields, &values, &a.SubmissionID, &a.SubmittedAt, &a.ReviewReason, &submitted); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(fields, &a.Fields); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(values, &a.Values); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(submitted, &a.SubmittedValues); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (s *Service) FormAssignment(ctx context.Context, p Principal, id int64) (FormAssignment, error) {
	// Read only the requested club identifier before applying the actor's scope.
	var clubID int32
	if err := s.DB.QueryRow(ctx, "SELECT club_id FROM junior_form_assignments WHERE id=$1", id).Scan(&clubID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FormAssignment{}, ErrNotFound
		}
		return FormAssignment{}, err
	}
	if err := Authorize(ctx, s.DB, p, clubID, false); err != nil {
		return FormAssignment{}, err
	}
	items, err := s.FormAssignments(ctx, p, 0)
	if err != nil {
		return FormAssignment{}, err
	}
	for _, a := range items {
		if a.ID == id {
			return a, nil
		}
	}
	return FormAssignment{}, ErrNotFound
}

func (s *Service) SaveFormResponse(ctx context.Context, p Principal, id int64, expectedRevision int, values map[string]string, submit bool) (int64, error) {
	if p.AdminID > 0 || p.ReadOnly {
		return 0, ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var clubID int32
	var state string
	var requiresReview bool
	var fieldsRaw []byte
	var currentSubmission int64
	err = tx.QueryRow(ctx, `SELECT a.club_id,a.status,v.requires_review,v.fields,COALESCE(a.latest_submission_id,0)
 FROM junior_form_assignments a JOIN junior_form_versions v ON v.id=a.version_id WHERE a.id=$1 FOR UPDATE OF a`, id).Scan(&clubID, &state, &requiresReview, &fieldsRaw, &currentSubmission)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return 0, err
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return 0, err
	}
	if submit && (state == "submitted" || state == "completed") {
		var identical bool
		err = tx.QueryRow(ctx, "SELECT values=$2::jsonb AND submitted_by_account_id=$3 FROM junior_form_submissions WHERE id=$1", currentSubmission, string(raw), p.AccountID).Scan(&identical)
		if err != nil {
			return 0, err
		}
		if identical {
			return currentSubmission, tx.Commit(ctx)
		}
		return 0, ErrConflict
	}
	if err = lockFormAssignmentSeasonOpen(ctx, tx, id); err != nil {
		return 0, err
	}
	if state != "assigned" && state != "returned" {
		return 0, ErrConflict
	}
	var fields []FormField
	if err = json.Unmarshal(fieldsRaw, &fields); err != nil {
		return 0, err
	}
	if HasTeamEntries(fields) {
		requiresReview = true
	}
	if err = ValidateFormValues(fields, values, submit); err != nil {
		return 0, err
	}
	var currentRevision int
	err = tx.QueryRow(ctx, "SELECT revision FROM junior_form_drafts WHERE assignment_id=$1 FOR UPDATE", id).Scan(&currentRevision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if currentRevision != expectedRevision {
		return 0, ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO junior_form_drafts(assignment_id,revision,values,updated_by_account_id) VALUES($1,$2,$3::jsonb,$4)
 ON CONFLICT(assignment_id) DO UPDATE SET revision=EXCLUDED.revision,values=EXCLUDED.values,updated_by_account_id=EXCLUDED.updated_by_account_id,updated_at=now()`, id, currentRevision+1, string(raw), p.AccountID)
	if err != nil {
		return 0, err
	}
	var submissionID int64
	if submit {
		err = tx.QueryRow(ctx, `INSERT INTO junior_form_submissions(assignment_id,revision,values,submitted_by_account_id)
  VALUES($1,(SELECT COALESCE(MAX(revision),0)+1 FROM junior_form_submissions WHERE assignment_id=$1),$2::jsonb,$3) RETURNING id`, id, string(raw), p.AccountID).Scan(&submissionID)
		if err != nil {
			return 0, err
		}
		status := "completed"
		if requiresReview {
			status = "submitted"
		}
		_, err = tx.Exec(ctx, "UPDATE junior_form_assignments SET status=$2,latest_submission_id=$3 WHERE id=$1", id, status, submissionID)
		if err != nil {
			return 0, err
		}
	}
	action := "form_draft_saved"
	if submit {
		action = "form_submitted"
	}
	if err = Audit(ctx, tx, p, clubID, "form_assignment", id, action, map[string]any{"submission_id": submissionID, "draft_revision": currentRevision + 1}); err != nil {
		return 0, err
	}
	return submissionID, tx.Commit(ctx)
}

func (s *Service) ReviewForm(ctx context.Context, p Principal, assignmentID, submissionID int64, action, reason string) error {
	if p.AdminID < 1 || p.ReadOnly {
		return ErrForbidden
	}
	if action != "complete" && action != "return" {
		return ErrValidation
	}
	if len(reason) > 2000 || strings.ContainsRune(reason, 0) || action == "return" && len(strings.TrimSpace(reason)) < 5 {
		return fmt.Errorf("%w: explain what the club needs to change", ErrValidation)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var clubID int32
	var latest int64
	var state string
	err = tx.QueryRow(ctx, "SELECT club_id,COALESCE(latest_submission_id,0),status FROM junior_form_assignments WHERE id=$1 FOR UPDATE", assignmentID).Scan(&clubID, &latest, &state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return err
	}
	if latest != submissionID || latest == 0 || state == "superseded" {
		return ErrConflict
	}
	if action == "complete" {
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT v.fields FROM junior_form_versions v JOIN junior_form_assignments a ON a.version_id=v.id WHERE a.id=$1`, assignmentID).Scan(&raw); err != nil {
			return err
		}
		var fields []FormField
		if err = json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		if HasTeamEntries(fields) {
			return fixtureValidation("review the team allocation preview before approving this entry form")
		}
	}
	target := "completed"
	if action == "return" {
		target = "returned"
	}
	if state == target {
		return tx.Commit(ctx)
	}
	if err = lockFormAssignmentSeasonOpen(ctx, tx, assignmentID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO junior_form_reviews(assignment_id,submission_id,action,reason,reviewed_by_admin_id) VALUES($1,$2,$3,$4,$5)", assignmentID, submissionID, action, reason, p.AdminID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE junior_form_assignments SET status=$2 WHERE id=$1", assignmentID, target); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, clubID, "form_assignment", assignmentID, "form_reviewed", map[string]any{"submission_id": submissionID, "action": action, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Keep closure and a real response/review mutation in a deterministic order.
// Identical completed submissions/reviews remain safe no-op retries.
func lockFormAssignmentSeasonOpen(ctx context.Context, q DBTX, assignmentID int64) error {
	var open bool
	err := q.QueryRow(ctx, `SELECT s.state='open' FROM junior_seasons s JOIN junior_forms f ON f.season_id=s.id JOIN junior_form_assignments a ON a.form_id=f.id WHERE a.id=$1 FOR SHARE OF s`, assignmentID).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !open {
		return fmt.Errorf("%w: this junior season is closed", ErrValidation)
	}
	return nil
}
