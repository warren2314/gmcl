package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/junior"
	"github.com/go-chi/chi/v5"
)

func juniorFormPathID(r *http.Request, key string) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, key), 10, 64)
	return id
}
func juniorFormLink(p junior.Principal, id int64) string {
	if p.ReadOnly {
		return fmt.Sprintf("/admin/juniors/clubs/%d/preview?view=forms&assignment_id=%d", p.ClubID, id)
	}
	if p.AdminID > 0 {
		return fmt.Sprintf("/admin/juniors/forms/assignments/%d", id)
	}
	return fmt.Sprintf("/junior/forms/%d", id)
}
func juniorFormListLink(p junior.Principal) string {
	if p.ReadOnly {
		return fmt.Sprintf("/admin/juniors/clubs/%d/preview?view=forms", p.ClubID)
	}
	if p.AdminID > 0 {
		return "/admin/juniors/forms"
	}
	return "/junior/forms"
}

// The builder exposes questions rather than a JSON configuration document.
// Blank slots are ignored; a populated slot retains its key in later versions.
func juniorParseFormBuilder(r *http.Request, id int64) (junior.FormInput, error) {
	input := junior.FormInput{ID: id, CurrentVersionID: juniorFixtureFormID(r, "current_version_id"), SeasonID: juniorFixtureFormID(r, "season_id"), Title: strings.TrimSpace(r.FormValue("title")), Description: r.FormValue("description"), RequiresReview: r.FormValue("requires_review") == "1"}
	due, err := time.Parse("2006-01-02", r.FormValue("due_date"))
	if err != nil {
		return input, fmt.Errorf("%w: enter a valid deadline", junior.ErrValidation)
	}
	input.DueDate = due
	for name := range r.PostForm {
		if strings.HasPrefix(name, "field_label_") {
			n, err := strconv.Atoi(strings.TrimPrefix(name, "field_label_"))
			if err != nil || n < 1 || n > 10 {
				return input, fmt.Errorf("%w: at most ten questions are supported", junior.ErrValidation)
			}
		}
	}
	for i := 1; i <= 10; i++ {
		suffix := strconv.Itoa(i)
		label := strings.TrimSpace(r.FormValue("field_label_" + suffix))
		choices := r.FormValue("field_options_" + suffix)
		required := r.FormValue("field_required_"+suffix) == "1"
		if label == "" {
			if required || strings.TrimSpace(choices) != "" {
				return input, fmt.Errorf("%w: add a label for question %d", junior.ErrValidation, i)
			}
			continue
		}
		key := r.FormValue("field_key_" + suffix)
		if key == "" {
			key = "question_" + suffix
		}
		field := junior.FormField{Key: key, Label: label, Type: r.FormValue("field_type_" + suffix), Required: required}
		if field.Type == "select" {
			for _, option := range strings.Split(strings.ReplaceAll(choices, "\r\n", "\n"), "\n") {
				if option = strings.TrimSpace(option); option != "" {
					field.Options = append(field.Options, option)
				}
			}
		} else if strings.TrimSpace(choices) != "" {
			return input, fmt.Errorf("%w: choices only apply to a choice question", junior.ErrValidation)
		}
		input.Fields = append(input.Fields, field)
	}
	if err = junior.ValidateFormFields(input.Fields); err != nil {
		return input, err
	}
	seen := map[int32]bool{}
	for _, raw := range r.PostForm["club_id"] {
		n, e := strconv.ParseInt(raw, 10, 32)
		if e != nil || n < 1 {
			return input, fmt.Errorf("%w: select valid target clubs", junior.ErrValidation)
		}
		club := int32(n)
		if !seen[club] {
			input.ClubIDs = append(input.ClubIDs, club)
			seen[club] = true
		}
	}
	return input, nil
}

func (s *Server) handleJuniorForms() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		svc := junior.NewService(s.DB)
		if p.AdminID < 1 || p.ReadOnly {
			juniorHTTPError(w, r, junior.ErrForbidden)
			return
		}
		id := juniorFormPathID(r, "formID")
		isNew := strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/new")
		if chi.URLParam(r, "formID") != "" && !isNew && id < 1 {
			juniorHTTPError(w, r, junior.ErrNotFound)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
			if err := r.ParseForm(); err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			switch r.FormValue("action") {
			case "remind":
				if id < 1 {
					juniorHTTPError(w, r, junior.ErrNotFound)
					return
				}
				count, err := svc.QueueFormReminders(r.Context(), p, id)
				if err != nil {
					juniorHTTPError(w, r, err)
					return
				}
				http.Redirect(w, r, fmt.Sprintf("/admin/juniors/forms/%d?success=%s", id, url.QueryEscape(fmt.Sprintf("Reminders prepared for %d outstanding club assignments with recipients. Delivery status is shown in Messages.", count))), http.StatusSeeOther)
				return
			case "publish":
				input, err := juniorParseFormBuilder(r, id)
				if err != nil {
					juniorHTTPError(w, r, err)
					return
				}
				saved, err := svc.PublishForm(r.Context(), p, input)
				if err != nil {
					juniorHTTPError(w, r, err)
					return
				}
				http.Redirect(w, r, fmt.Sprintf("/admin/juniors/forms/%d?success=%s", saved, url.QueryEscape("Official form published. Earlier submitted responses remain in history.")), http.StatusSeeOther)
				return
			default:
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
		}
		seasons, seasonID, err := s.juniorRecordSeason(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		forms, err := svc.ListForms(r.Context(), p, 0)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if len(seasons) == 0 && id == 0 {
			s.juniorPageStart(w, r, "Official junior forms")
			defer juniorPageEnd(w)
			fmt.Fprint(w, `<p>No junior season is configured yet. Configure its dates and participating clubs before publishing an official form.</p><a class="btn btn-primary" href="/admin/juniors/clubs">Configure the first junior season</a>`)
			return
		}
		if id == 0 && !isNew {
			if status := r.URL.Query().Get("status"); status != "" {
				items, err := svc.FormAssignments(r.Context(), p, 0)
				if err != nil {
					juniorHTTPError(w, r, err)
					return
				}
				items = juniorScopeFormAssignments(items, seasonID)
				items = juniorFilterFormAssignments(items, status, r.URL.Query().Get("q"))
				if r.URL.Query().Get("export") == "csv" {
					juniorFixtureCSV(w, "junior-form-responses.csv", juniorFormCSVRows(items))
					return
				}
				s.juniorPageStart(w, r, "Official junior form assignments")
				defer juniorPageEnd(w)
				juniorFormSeasonSelector(w, r, seasons, seasonID)
				juniorRenderFormFilter(w, r, false)
				export := *r.URL
				query := export.Query()
				query.Set("export", "csv")
				export.RawQuery = query.Encode()
				fmt.Fprintf(w, `<p>%d matching club assignments. <a href="%s">Export matching responses</a></p><p><a href="/admin/juniors/forms?season_id=%d">All official forms in this season</a></p>`, len(items), escapeHTML(export.String()), seasonID)
				juniorRenderFormAssignmentTable(w, p, items)
				return
			}
			s.juniorPageStart(w, r, "Official junior forms")
			defer juniorPageEnd(w)
			juniorFormSeasonSelector(w, r, seasons, seasonID)
			fmt.Fprintf(w, `<p>Publish a versioned form to selected clubs, track submissions and review responses. Overdue is a subset of outstanding.</p><a class="btn btn-primary mb-3" href="/admin/juniors/forms/new?season_id=%d">Create official form</a><div class="table-responsive"><table class="table"><thead><tr><th>Form</th><th>Deadline</th><th>Outstanding</th><th>Overdue</th><th>For review</th><th>Completed</th></tr></thead><tbody>`, seasonID)
			count := 0
			for _, f := range forms {
				if seasonID > 0 && f.SeasonID != seasonID {
					continue
				}
				count++
				fmt.Fprintf(w, `<tr><td><a href="/admin/juniors/forms/%d">%s</a><div class="small text-muted">%s · version %d</div></td><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td></tr>`, f.ID, escapeHTML(f.Title), escapeHTML(f.Season), f.Revision, f.DueDate.Format("2006-01-02"), f.Outstanding, f.Overdue, f.RequiresReview, f.Completed)
			}
			if count == 0 {
				fmt.Fprint(w, `<tr><td colspan="6">No official forms in this junior season.</td></tr>`)
			}
			fmt.Fprint(w, `</tbody></table></div>`)
			return
		}
		input := junior.FormInput{SeasonID: seasonID, RequiresReview: true}
		assignments := []junior.FormAssignment{}
		if id > 0 {
			var summary *junior.FormSummary
			for i := range forms {
				if forms[i].ID == id {
					summary = &forms[i]
					break
				}
			}
			if summary == nil {
				juniorHTTPError(w, r, junior.ErrNotFound)
				return
			}
			assignments, err = svc.FormAssignments(r.Context(), p, id)
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			input.ID, input.CurrentVersionID, input.SeasonID, input.Title, input.DueDate = id, summary.VersionID, summary.SeasonID, summary.Title, summary.DueDate
			for _, a := range assignments {
				if a.VersionID == summary.VersionID {
					input.Fields = a.Fields
					input.Description = a.Description
					input.RequiresReview = a.RequiresReview
					input.ClubIDs = append(input.ClubIDs, a.ClubID)
				}
			}
		}
		if isNew || r.URL.Query().Get("edit") == "1" {
			clubs, err := svc.ListClubs(r.Context(), p, input.SeasonID)
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			s.juniorPageStart(w, r, "Publish official form")
			defer juniorPageEnd(w)
			if isNew {
				juniorFormSeasonSelector(w, r, seasons, input.SeasonID)
			}
			s.renderJuniorFormBuilder(w, r, input, clubs, seasons)
			return
		}
		filtered := juniorFilterFormAssignments(assignments, r.URL.Query().Get("status"), r.URL.Query().Get("q"))
		if r.URL.Query().Get("export") == "csv" {
			juniorFixtureCSV(w, "junior-form-responses.csv", juniorFormCSVRows(filtered))
			return
		}
		s.juniorPageStart(w, r, input.Title)
		defer juniorPageEnd(w)
		fmt.Fprintf(w, `<p>Current version deadline: %s. Submitted responses retain their original version and deadline.</p><div class="d-flex flex-wrap gap-2 mb-3"><a class="btn btn-outline-primary" href="/admin/juniors/forms/%d?edit=1">Publish a new version</a><form method="POST">`, input.DueDate.Format("2006-01-02"), id)
		juniorFixtureCSRF(w, r)
		fmt.Fprint(w, `<input type="hidden" name="action" value="remind"><button class="btn btn-outline-secondary">Remind outstanding clubs</button></form></div>`)
		juniorRenderFormFilter(w, r, false)
		export := *r.URL
		query := export.Query()
		query.Set("export", "csv")
		export.RawQuery = query.Encode()
		fmt.Fprintf(w, `<p>%d matching club assignments. <a href="%s">Export matching responses</a></p>`, len(filtered), escapeHTML(export.String()))
		juniorRenderFormAssignmentTable(w, p, filtered)
	}
}

func (s *Server) renderJuniorFormBuilder(w http.ResponseWriter, r *http.Request, input junior.FormInput, clubs []junior.ClubSummary, seasons []junior.Season) {
	open := false
	for _, season := range seasons {
		if season.ID == input.SeasonID {
			fmt.Fprintf(w, `<p>Junior season: <strong>%s</strong></p>`, escapeHTML(season.Name))
			open = season.State == "open"
		}
	}
	if !open {
		fmt.Fprint(w, `<div class="alert alert-warning">Choose an open junior season before publishing a form.</div>`)
		return
	}
	fmt.Fprint(w, `<p>Use up to ten questions. Open an unused question to add another. Blank questions are omitted. Each publication fixes its questions, deadline and selected clubs.</p><form method="POST">`)
	juniorFixtureCSRF(w, r)
	fmt.Fprintf(w, `<input type="hidden" name="action" value="publish"><input type="hidden" name="season_id" value="%d"><input type="hidden" name="current_version_id" value="%d"><div class="row g-3 mb-3"><label class="col-md-8">Form title<input class="form-control" name="title" maxlength="200" value="%s" required></label><label class="col-md-4">Deadline<input class="form-control" type="date" name="due_date" value="%s" required></label><label class="col-12">Instructions<textarea class="form-control" name="description" maxlength="4000" rows="3">%s</textarea></label></div>`, input.SeasonID, input.CurrentVersionID, escapeHTML(input.Title), func() string {
		if input.DueDate.IsZero() {
			return ""
		}
		return input.DueDate.Format("2006-01-02")
	}(), escapeHTML(input.Description))
	checked := ""
	if input.RequiresReview {
		checked = " checked"
	}
	fmt.Fprintf(w, `<label class="form-check mb-3"><input class="form-check-input" type="checkbox" name="requires_review" value="1"%s>Require league review after submission</label>`, checked)
	usedKeys := map[string]bool{}
	for _, field := range input.Fields {
		usedKeys[field.Key] = true
	}
	for i := 1; i <= 10; i++ {
		key := fmt.Sprintf("question_%d", i)
		if i > len(input.Fields) {
			for n := 1; usedKeys[key]; n++ {
				key = fmt.Sprintf("question_%d", n)
			}
			usedKeys[key] = true
		}
		f := junior.FormField{Key: key, Type: "text"}
		if i <= len(input.Fields) {
			f = input.Fields[i-1]
		}
		opened := ""
		if i == 1 || f.Label != "" {
			opened = " open"
		}
		label := f.Label
		if label == "" {
			label = "Unused question"
		}
		fmt.Fprintf(w, `<details class="card mb-2"%s><summary class="card-header">Question %d · %s</summary><div class="card-body row g-3"><input type="hidden" name="field_key_%d" value="%s"><label class="col-md-8">Question label<input class="form-control" name="field_label_%d" maxlength="160" value="%s"></label><label class="col-md-4">Answer type<select class="form-select" name="field_type_%d">`, opened, i, escapeHTML(label), i, escapeHTML(f.Key), i, escapeHTML(f.Label), i)
		for _, kind := range []struct{ key, label string }{{"text", "Short text"}, {"long_text", "Long text"}, {"email", "Email address"}, {"number", "Number"}, {"date", "Date"}, {"yes_no", "Yes / no"}, {"select", "Choice"}} {
			sel := ""
			if kind.key == f.Type {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, kind.key, sel, kind.label)
		}
		checked = ""
		if f.Required {
			checked = " checked"
		}
		fmt.Fprintf(w, `</select></label><label class="col-md-8">Choices — one per line, for Choice answers only<textarea class="form-control" name="field_options_%d" rows="2">%s</textarea></label><div class="col-md-4"><label class="form-check mt-4"><input class="form-check-input" type="checkbox" name="field_required_%d" value="1"%s>Answer required</label></div></div></details>`, i, escapeHTML(strings.Join(f.Options, "\n")), i, checked)
	}
	selected := map[int32]bool{}
	for _, id := range input.ClubIDs {
		selected[id] = true
	}
	count := 0
	fmt.Fprint(w, `<fieldset class="border rounded p-3 my-3"><legend class="h5">Target clubs</legend><p class="small text-muted">Only clubs with active junior participation in this season can be assigned.</p><div class="row g-2">`)
	for _, club := range clubs {
		if club.State != "active" {
			continue
		}
		count++
		checked = ""
		if selected[club.ID] {
			checked = " checked"
		}
		fmt.Fprintf(w, `<div class="col-sm-6 col-lg-4"><label class="form-check"><input class="form-check-input" type="checkbox" name="club_id" value="%d"%s>%s</label></div>`, club.ID, checked, escapeHTML(club.Name))
	}
	fmt.Fprint(w, `</div></fieldset>`)
	if input.ID > 0 {
		fmt.Fprint(w, `<div class="alert alert-info">Publishing creates a new version. Outstanding drafts from the earlier version are superseded; submitted and completed responses remain available in history.</div>`)
	}
	if count > 0 {
		fmt.Fprint(w, `<button class="btn btn-primary">Publish official form to selected clubs</button>`)
	} else {
		fmt.Fprint(w, `<div class="alert alert-warning">Activate junior participation for at least one club before publishing.</div>`)
	}
	fmt.Fprint(w, `</form>`)
}

func juniorFilterFormAssignments(items []junior.FormAssignment, status, query string) []junior.FormAssignment {
	out := []junior.FormAssignment{}
	query = strings.ToLower(strings.TrimSpace(query))
	for _, a := range items {
		matches := status == "" || a.Status == status
		switch status {
		case "outstanding":
			matches = a.Status == "assigned" || a.Status == "returned"
		case "overdue":
			matches = (a.Status == "assigned" || a.Status == "returned") && junior.FormAssignmentStatus(a, time.Now()) == "Overdue"
		case "review":
			matches = a.Status == "submitted"
		}
		if !matches {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(a.Club+" "+a.Title), query) {
			continue
		}
		out = append(out, a)
	}
	return out
}
func juniorScopeFormAssignments(items []junior.FormAssignment, seasonID int64) []junior.FormAssignment {
	if seasonID == 0 {
		return items
	}
	out := []junior.FormAssignment{}
	for _, a := range items {
		if a.SeasonID == seasonID {
			out = append(out, a)
		}
	}
	return out
}
func juniorFormStatus(status string) string {
	switch status {
	case "outstanding":
		return "Outstanding"
	case "overdue":
		return "Overdue"
	case "review":
		return "Requires league review"
	case "assigned":
		return "To complete"
	case "returned":
		return "Returned for changes"
	case "submitted":
		return "Awaiting league review"
	case "completed":
		return "Completed"
	case "superseded":
		return "Earlier version — superseded"
	}
	return status
}
func juniorFormSeasonSelector(w http.ResponseWriter, r *http.Request, seasons []junior.Season, selected int64) {
	fmt.Fprint(w, `<form method="GET" class="d-flex gap-2 align-items-end flex-wrap mb-3">`)
	for _, key := range []string{"status", "q"} {
		if value := r.URL.Query().Get(key); value != "" {
			fmt.Fprintf(w, `<input type="hidden" name="%s" value="%s">`, key, escapeHTML(value))
		}
	}
	fmt.Fprint(w, `<label>Junior season<select class="form-select" name="season_id">`)
	for _, season := range seasons {
		sel := ""
		if season.ID == selected {
			sel = " selected"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s · %s</option>`, season.ID, sel, escapeHTML(season.Name), escapeHTML(season.State))
	}
	fmt.Fprint(w, `</select></label><button class="btn btn-outline-primary">Open season</button></form>`)
}
func juniorRenderFormFilter(w http.ResponseWriter, r *http.Request, preview bool) {
	fmt.Fprint(w, `<form method="GET" class="d-flex flex-wrap gap-2 align-items-end mb-3">`)
	if preview {
		fmt.Fprint(w, `<input type="hidden" name="view" value="forms">`)
	}
	if id := juniorQueryID(r, "season_id"); id > 0 {
		fmt.Fprintf(w, `<input type="hidden" name="season_id" value="%d">`, id)
	}
	fmt.Fprintf(w, `<label>Club or form<input class="form-control" name="q" value="%s"></label><label>Status<select class="form-select" name="status"><option value="">All assignments and history</option>`, escapeHTML(r.URL.Query().Get("q")))
	for _, status := range []string{"outstanding", "overdue", "review", "assigned", "returned", "submitted", "completed", "superseded"} {
		sel := ""
		if r.URL.Query().Get("status") == status {
			sel = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, status, sel, escapeHTML(juniorFormStatus(status)))
	}
	fmt.Fprint(w, `</select></label><button class="btn btn-outline-primary">Apply filters</button></form>`)
}
func juniorRenderFormAssignmentTable(w http.ResponseWriter, p junior.Principal, items []junior.FormAssignment) {
	fmt.Fprint(w, `<div class="table-responsive"><table class="table"><thead><tr><th>Form / club</th><th>Version</th><th>Deadline</th><th>Status</th><th>Submission receipt</th></tr></thead><tbody>`)
	for _, a := range items {
		receipt := "Not submitted"
		if a.SubmissionID > 0 {
			receipt = fmt.Sprintf("Receipt %d", a.SubmissionID)
		}
		fmt.Fprintf(w, `<tr><td><a href="%s">%s</a><div class="small">%s</div></td><td>%d</td><td>%s</td><td>%s</td><td>%s</td></tr>`, escapeHTML(juniorFormLink(p, a.ID)), escapeHTML(a.Title), escapeHTML(a.Club), a.Revision, a.DueDate.Format("2006-01-02"), escapeHTML(junior.FormAssignmentStatus(a, time.Now())), escapeHTML(receipt))
	}
	if len(items) == 0 {
		fmt.Fprint(w, `<tr><td colspan="5">No matching form assignments.</td></tr>`)
	}
	fmt.Fprint(w, `</tbody></table></div>`)
}
func juniorFormCSVRows(items []junior.FormAssignment) [][]string {
	rows := [][]string{{"assignment_id", "form", "club", "version", "status", "deadline", "submission_id", "question_key", "question", "submitted_answer", "draft_answer"}}
	for _, a := range items {
		for _, f := range a.Fields {
			rows = append(rows, []string{strconv.FormatInt(a.ID, 10), a.Title, a.Club, strconv.Itoa(a.Revision), a.Status, a.DueDate.Format("2006-01-02"), strconv.FormatInt(a.SubmissionID, 10), f.Key, f.Label, a.SubmittedValues[f.Key], a.Values[f.Key]})
		}
	}
	return rows
}

func (s *Server) handleJuniorClubForms() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		id := juniorFormPathID(r, "assignmentID")
		if id == 0 {
			id = juniorQueryID(r, "assignment_id")
		}
		if id > 0 {
			s.handleJuniorFormAssignment()(w, r)
			return
		}
		if r.Method != http.MethodGet {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		items, err := junior.NewService(s.DB).FormAssignments(r.Context(), p, 0)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		items = juniorScopeFormAssignments(items, juniorQueryID(r, "season_id"))
		items = juniorFilterFormAssignments(items, r.URL.Query().Get("status"), r.URL.Query().Get("q"))
		if r.URL.Query().Get("export") == "csv" {
			juniorFixtureCSV(w, "junior-club-form-responses.csv", juniorFormCSVRows(items))
			return
		}
		s.juniorPageStart(w, r, "Your official junior forms")
		defer juniorPageEnd(w)
		fmt.Fprint(w, `<p>Save a draft while gathering answers, then submit to receive a permanent receipt. Earlier versions and submitted responses stay available here.</p>`)
		juniorRenderFormFilter(w, r, p.ReadOnly)
		juniorRenderFormAssignmentTable(w, p, items)
	}
}

func (s *Server) handleJuniorFormAssignment() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		svc := junior.NewService(s.DB)
		id := juniorFormPathID(r, "assignmentID")
		if id == 0 {
			id = juniorQueryID(r, "assignment_id")
		}
		assignment, err := svc.FormAssignment(r.Context(), p, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if r.Method == http.MethodPost {
			if p.ReadOnly {
				juniorHTTPError(w, r, junior.ErrForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
			if err = r.ParseForm(); err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			if p.AdminID > 0 {
				err = svc.ReviewForm(r.Context(), p, id, juniorFixtureFormID(r, "submission_id"), r.FormValue("action"), r.FormValue("reason"))
			} else {
				action := r.FormValue("action")
				if action != "save" && action != "submit" {
					juniorHTTPError(w, r, junior.ErrValidation)
					return
				}
				revision, e := strconv.Atoi(r.FormValue("draft_revision"))
				if e != nil || revision < 0 {
					juniorHTTPError(w, r, junior.ErrValidation)
					return
				}
				values := map[string]string{}
				for _, field := range assignment.Fields {
					values[field.Key] = strings.TrimSpace(r.FormValue("answer_" + field.Key))
				}
				_, err = svc.SaveFormResponse(r.Context(), p, id, revision, values, action == "submit")
			}
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			http.Redirect(w, r, juniorFormLink(p, id)+"?success="+url.QueryEscape("Saved. Any submitted response has a permanent receipt below."), http.StatusSeeOther)
			return
		}
		receipts, err := svc.FormSubmissionHistory(r.Context(), p, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, assignment.Title)
		defer juniorPageEnd(w)
		back := juniorFormListLink(p)
		separator := "?"
		if strings.Contains(back, "?") {
			separator = "&"
		}
		if assignment.SeasonID > 0 {
			back += fmt.Sprintf("%sseason_id=%d", separator, assignment.SeasonID)
		}
		if p.AdminID > 0 && !p.ReadOnly {
			back = fmt.Sprintf("/admin/juniors/forms/%d", assignment.FormID)
		}
		fmt.Fprintf(w, `<a href="%s">Back to forms</a><p class="mt-3">%s · version %d · deadline %s · <strong>%s</strong></p><p style="white-space:pre-wrap">%s</p>`, escapeHTML(back), escapeHTML(assignment.Club), assignment.Revision, assignment.DueDate.Format("2006-01-02"), escapeHTML(junior.FormAssignmentStatus(assignment, time.Now())), escapeHTML(assignment.Description))
		if assignment.ReviewReason != "" {
			fmt.Fprintf(w, `<div class="alert alert-info"><strong>Latest league review:</strong> <span style="white-space:pre-wrap">%s</span></div>`, escapeHTML(assignment.ReviewReason))
		}
		editable := p.AdminID == 0 && !p.ReadOnly && (assignment.Status == "assigned" || assignment.Status == "returned")
		if editable {
			fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Your draft</h2><p>Saving keeps your draft private to this club and authorised league staff. Submit when the answers are ready.</p><form method="POST">`)
			juniorFixtureCSRF(w, r)
			fmt.Fprintf(w, `<input type="hidden" name="draft_revision" value="%d">`, assignment.DraftRevision)
			for _, field := range assignment.Fields {
				juniorRenderFormAnswer(w, field, assignment.Values[field.Key])
			}
			fmt.Fprint(w, `<div class="d-flex gap-2 flex-wrap"><button class="btn btn-outline-primary" name="action" value="save" formnovalidate>Save draft</button><button class="btn btn-primary" name="action" value="submit">Submit official response</button></div></form></div></section>`)
		} else if assignment.Status == "assigned" || assignment.Status == "returned" {
			fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Current draft — read-only</h2>`)
			juniorRenderFormValues(w, assignment.Fields, assignment.Values)
			fmt.Fprint(w, `</div></section>`)
		}
		if p.AdminID > 0 && !p.ReadOnly && assignment.SubmissionID > 0 && assignment.Status == "submitted" {
			fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Review submitted response</h2><form method="POST">`)
			juniorFixtureCSRF(w, r)
			fmt.Fprintf(w, `<input type="hidden" name="submission_id" value="%d"><label class="form-label" for="review-reason">Review note (required when returning)</label><textarea class="form-control mb-3" name="reason" id="review-reason" maxlength="2000" rows="3"></textarea><div class="d-flex flex-wrap gap-2"><button class="btn btn-success" name="action" value="complete">Mark completed</button><button class="btn btn-outline-primary" name="action" value="return">Return to club for changes</button></div></form></div></section>`, assignment.SubmissionID)
		}
		fmt.Fprint(w, `<section><h2 class="h4">Submitted response history</h2>`)
		if len(receipts) == 0 {
			fmt.Fprint(w, `<p>No submitted response yet.</p>`)
		}
		for _, receipt := range receipts {
			fmt.Fprintf(w, `<details class="card mb-3" open><summary class="card-header">Receipt %d · submission %d · %s</summary><div class="card-body">`, receipt.ID, receipt.Revision, receipt.SubmittedAt.In(juniorLondon()).Format("2 Jan 2006 15:04 MST"))
			juniorRenderFormValues(w, assignment.Fields, receipt.Values)
			fmt.Fprint(w, `</div></details>`)
		}
		fmt.Fprint(w, `</section>`)
	}
}
func juniorLondon() *time.Location {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		return time.UTC
	}
	return loc
}
func juniorRenderFormValues(w http.ResponseWriter, fields []junior.FormField, values map[string]string) {
	fmt.Fprint(w, `<dl>`)
	for _, f := range fields {
		value := values[f.Key]
		if value == "" {
			value = "Not answered"
		}
		fmt.Fprintf(w, `<dt>%s</dt><dd style="white-space:pre-wrap;overflow-wrap:anywhere">%s</dd>`, escapeHTML(f.Label), escapeHTML(value))
	}
	fmt.Fprint(w, `</dl>`)
}
func juniorRenderFormAnswer(w http.ResponseWriter, f junior.FormField, value string) {
	id := "answer_" + f.Key
	required := ""
	star := ""
	if f.Required {
		required = " required"
		star = " (required)"
	}
	fmt.Fprintf(w, `<div class="mb-3"><label class="form-label" for="%s">%s%s</label>`, escapeHTML(id), escapeHTML(f.Label), star)
	switch f.Type {
	case "long_text":
		fmt.Fprintf(w, `<textarea id="%s" class="form-control" name="%s" maxlength="4000" rows="4"%s>%s</textarea>`, escapeHTML(id), escapeHTML(id), required, escapeHTML(value))
	case "select", "yes_no":
		fmt.Fprintf(w, `<select id="%s" class="form-select" name="%s"%s><option value="">Choose an answer</option>`, escapeHTML(id), escapeHTML(id), required)
		choices := f.Options
		if f.Type == "yes_no" {
			choices = []string{"yes", "no"}
		}
		for _, choice := range choices {
			sel := ""
			if choice == value {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(choice), sel, escapeHTML(choice))
		}
		fmt.Fprint(w, `</select>`)
	default:
		kind := f.Type
		if kind != "email" && kind != "number" && kind != "date" {
			kind = "text"
		}
		step := ""
		if kind == "number" {
			step = ` step="any"`
		}
		fmt.Fprintf(w, `<input id="%s" class="form-control" type="%s" name="%s" value="%s" maxlength="4000"%s%s>`, escapeHTML(id), kind, escapeHTML(id), escapeHTML(value), step, required)
	}
	fmt.Fprint(w, `</div>`)
}
