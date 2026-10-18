package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func juniorRecordsSeason(t *testing.T, ctx context.Context, service *junior.Service, p junior.Principal, name, state string) int64 {
	t.Helper()
	start := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	if name == "Previous junior season" {
		start = start.AddDate(-1, 0, 0)
	}
	id, err := service.SaveSeason(ctx, p, junior.SeasonInput{Name: name, State: state, StartDate: start, EndDate: start.AddDate(0, 6, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestJuniorRecordsTwoSeasonHistoryAndClubScopePostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	admin, clubA, clubB := seedJuniorAccessAdminClub(t, ctx, pool)
	service := junior.NewService(pool)
	staff := junior.Principal{AdminID: admin}
	previous := juniorRecordsSeason(t, ctx, service, staff, "Previous junior season", "closed")
	next := juniorRecordsSeason(t, ctx, service, staff, "Next junior season", "open")
	if err := service.SetParticipation(ctx, staff, clubA, previous, "active", "Previous entry"); err != nil {
		t.Fatal(err)
	}
	if err := service.SetParticipation(ctx, staff, clubA, next, "withdrawn", "Staff review"); err != nil {
		t.Fatal(err)
	}
	team, err := service.SaveTeam(ctx, staff, junior.TeamInput{ClubID: clubA, Name: "U11 A"})
	if err != nil {
		t.Fatal(err)
	}
	otherTeam, err := service.SaveTeam(ctx, staff, junior.TeamInput{ClubID: clubB, Name: "U13 A"})
	if err != nil {
		t.Fatal(err)
	}
	starts := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	in := junior.ContactInput{ClubID: clubA, SeasonID: next, FullName: "Alex Adult", Email: "alex@example.invalid", Role: "junior_lead", StartsOn: starts}
	contact, err := service.SaveContact(ctx, staff, in)
	if err != nil {
		t.Fatal(err)
	}
	futureStart := time.Now().AddDate(0, 1, 0)
	futureContact, err := service.SaveContact(ctx, staff, junior.ContactInput{ClubID: clubB, SeasonID: next, FullName: "Future Lead", Email: "future@example.invalid", Role: "junior_lead", StartsOn: futureStart})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveContact(ctx, staff, junior.ContactInput{ID: futureContact, ClubID: clubB, SeasonID: next, FullName: "Future Lead", Phone: "0123456789"}); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("future lead lost email through details-only edit: %v", err)
	}
	var futureEmail string
	if err = pool.QueryRow(ctx, `SELECT email FROM junior_contacts WHERE id=$1`, futureContact).Scan(&futureEmail); err != nil || futureEmail != "future@example.invalid" {
		t.Fatalf("failed identity edit persisted: %q %v", futureEmail, err)
	}
	in.ID = contact
	in.Role = "coach"
	in.AgeGroup = "U11"
	in.TeamID = &team
	if _, err = service.SaveContact(ctx, staff, in); err != nil {
		t.Fatal(err)
	}
	profile, err := service.ClubProfile(ctx, staff, clubA, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Participation) != 2 || profile.Club.State != "withdrawn" || len(profile.Contacts) != 1 || len(profile.Contacts[0].Roles) != 2 {
		t.Fatalf("history/multiple roles lost: %+v", profile)
	}
	in.TeamID = &otherTeam
	if _, err = service.SaveContact(ctx, staff, in); !errors.Is(err, junior.ErrNotFound) {
		t.Fatalf("foreign team accepted: %v", err)
	}
	replacement := junior.ContactInput{ClubID: clubA, SeasonID: next, FullName: "Replacement Adult", Email: "replacement@example.invalid", Role: "junior_lead", StartsOn: starts}
	if _, err = service.SaveContact(ctx, staff, replacement); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("overlapping lead accepted: %v", err)
	}
	var leadRole int64
	for _, role := range profile.Contacts[0].Roles {
		if role.Role == "junior_lead" {
			leadRole = role.ID
		}
	}
	end := starts.AddDate(0, 0, 7)
	if err = service.EndContactRole(ctx, staff, clubA, leadRole, end); err != nil {
		t.Fatal(err)
	}
	replacement.StartsOn = end.AddDate(0, 0, 1)
	if _, err = service.SaveContact(ctx, staff, replacement); err != nil {
		t.Fatal(err)
	}
	grant := seedJuniorAccess(t, pool, clubA, admin, "portal@example.invalid")
	if _, err = service.ClubProfile(ctx, grant.Principal, clubB, next); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("other-club read: %v", err)
	}
	in.TeamID = &team
	in.SeasonID = previous
	if _, err = service.SaveContact(ctx, grant.Principal, in); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("closed-season lead write: %v", err)
	}
	preview := junior.Principal{AdminID: admin, ClubID: clubA, ReadOnly: true}
	if _, err = service.SaveTeam(ctx, preview, junior.TeamInput{ClubID: clubA, Name: "Preview write"}); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("preview write: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE junior_events SET action='changed'`); err == nil {
		t.Fatal("audit events were mutable")
	}
}

func TestJuniorBadgePrivateImmutableRevisionsPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	admin, clubA, clubB := seedJuniorAccessAdminClub(t, ctx, pool)
	service := junior.NewService(pool)
	staff := junior.Principal{AdminID: admin}
	t.Setenv("JUNIOR_UPLOAD_DIR", t.TempDir())
	badge := func(red uint8) []byte {
		picture := image.NewRGBA(image.Rect(0, 0, 3, 3))
		picture.Set(0, 0, color.RGBA{R: red, A: 255})
		var out bytes.Buffer
		if err := png.Encode(&out, picture); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	first, err := service.SaveBadge(ctx, staff, clubA, "original.png", badge(10))
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.SaveBadge(ctx, staff, clubA, "same.png", badge(10))
	if err != nil || again.ID != first.ID {
		t.Fatalf("identical revision duplicated: %+v %v", again, err)
	}
	second, err := service.SaveBadge(ctx, staff, clubA, "new.png", badge(20))
	if err != nil || second.ID == first.ID {
		t.Fatalf("new revision missing: %+v %v", second, err)
	}
	profile, err := service.ClubProfile(ctx, staff, clubA, 0)
	if err != nil || profile.BadgeID != second.ID || len(profile.Badges) != 2 {
		t.Fatalf("selected badge/history: %+v %v", profile, err)
	}
	if _, data, err := service.ReadBadge(ctx, staff, clubA, first.ID); err != nil || len(data) == 0 {
		t.Fatalf("old badge inaccessible: %v", err)
	}
	grant := seedJuniorAccess(t, pool, clubB, admin, "otherclub@example.invalid")
	if _, _, err = service.ReadBadge(ctx, grant.Principal, clubA, first.ID); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("badge cross-club read: %v", err)
	}
	if _, err = service.SaveBadge(ctx, staff, clubA, "fake.svg", []byte("<svg></svg>")); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("SVG accepted: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE junior_badge_revisions SET original_name='changed' WHERE id=$1`, first.ID); err == nil {
		t.Fatal("badge revision mutable")
	}
}

func TestJuniorRosterExplicitMappingAtomicIdempotentApplyPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	admin, clubA, clubB := seedJuniorAccessAdminClub(t, ctx, pool)
	service := junior.NewService(pool)
	staff := junior.Principal{AdminID: admin}
	season := juniorRecordsSeason(t, ctx, service, staff, "Next junior season", "open")
	existing, err := service.SaveContact(ctx, staff, junior.ContactInput{ClubID: clubA, SeasonID: season, FullName: "Existing identity", Email: "existing@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("club_name,contact_name,email,phone,role,age_group,team_name\nSynthetic Junior Club A,Source identity,source@example.invalid,,coach,U11,U11 A\nUnresolved club,,,,,,\nSynthetic Junior Club A,New Adult,new@example.invalid,,safeguarding_officer,,\n")
	if _, err = pool.Exec(ctx, `INSERT INTO clubs(name) VALUES('SYNTHETIC JUNIOR CLUB A')`); err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewRoster(ctx, staff, season, "previous.csv", source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Rows[0].SuggestedClubID != 0 || len(preview.Rows[0].CandidateClubIDs) != 2 || preview.Rows[1].SuggestedClubID != 0 || preview.State != "preview" {
		t.Fatalf("unsafe match suggestion: %+v", preview)
	}
	var before int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_club_seasons`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("preview changed participation: %d %v", before, err)
	}
	if _, err = service.ApplyRoster(ctx, staff, preview.ID, nil); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("unreviewed apply accepted: %v", err)
	}
	badReview := []junior.RosterMapping{{RowNumber: 1, ClubID: clubA}, {RowNumber: 2}, {RowNumber: 3, ClubID: clubA}}
	if _, err = service.ReviewRoster(ctx, staff, preview.ID, badReview); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("unresolved row accepted: %v", err)
	}
	review := []junior.RosterMapping{{RowNumber: 1, ClubID: clubA}, {RowNumber: 2, Skip: true, Reason: "No confirmed existing club"}, {RowNumber: 3, ClubID: clubA}}
	if _, err = service.ReviewRoster(ctx, staff, preview.ID, review); err != nil {
		t.Fatal(err)
	}
	foreign, err := service.SaveContact(ctx, staff, junior.ContactInput{ClubID: clubB, SeasonID: season, FullName: "Foreign Adult", Email: "foreign@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	apply := append([]junior.RosterMapping(nil), review...)
	apply[0].ContactID = foreign
	apply[0].CreateTeam = true
	apply[2].CreateContact = true
	if _, err = service.ApplyRoster(ctx, staff, preview.ID, apply); !errors.Is(err, junior.ErrNotFound) {
		t.Fatalf("foreign identity accepted: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_club_seasons`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("failed apply partially committed: %d %v", before, err)
	}
	apply[0].ContactID = existing
	result, err := service.ApplyRoster(ctx, staff, preview.ID, apply)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsApplied != 2 || result.RowsSkipped != 1 || result.ClubsActivated != 1 || result.ContactsCreated != 1 || result.TeamsCreated != 1 || result.RolesAdded != 2 {
		t.Fatalf("unexpected receipt: %+v", result)
	}
	again, err := service.ApplyRoster(ctx, staff, preview.ID, apply)
	if err != nil || again != result {
		t.Fatalf("apply retry not idempotent: %+v %v", again, err)
	}
	profile, err := service.ClubProfile(ctx, staff, clubA, season)
	if err != nil {
		t.Fatal(err)
	}
	for _, contact := range profile.Contacts {
		if contact.ID == existing && (contact.FullName != "Existing identity" || contact.Email != "existing@example.invalid") {
			t.Fatalf("reused identity overwritten: %+v", contact)
		}
	}
	var provenance, rows, audits int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM junior_club_seasons WHERE provenance->>'import_id'=$1),(SELECT count(*) FROM junior_roster_applied_rows WHERE import_id=$2),(SELECT count(*) FROM junior_events WHERE entity_type='roster_import' AND entity_id=$2 AND action='roster_applied')`, fmt.Sprint(preview.ID), preview.ID).Scan(&provenance, &rows, &audits); err != nil || provenance != 1 || rows != 3 || audits != 1 {
		t.Fatalf("provenance/receipt %d/%d/%d: %v", provenance, rows, audits, err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM junior_roster_applied_rows WHERE import_id=$1`, preview.ID); err == nil {
		t.Fatal("roster provenance deleted")
	}
	if _, err = pool.Exec(ctx, `UPDATE junior_roster_imports SET source_rows='[]' WHERE id=$1`, preview.ID); err == nil {
		t.Fatal("roster source mutated")
	}
	repeated, err := service.PreviewRoster(ctx, staff, season, "same-source.csv", source)
	if err != nil || repeated.ID != preview.ID || repeated.State != "applied" {
		t.Fatalf("re-upload lost original receipt: %+v %v", repeated, err)
	}
}

func TestJuniorRecordsHTTPPreviewRosterAndBadgeWorkflowPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	admin, clubA, clubB := seedJuniorAccessAdminClub(t, ctx, pool)
	service := junior.NewService(pool)
	staff := junior.Principal{AdminID: admin}
	season := juniorRecordsSeason(t, ctx, service, staff, "Next junior season", "open")
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-records-http-secret")
	t.Setenv("JUNIOR_UPLOAD_DIR", t.TempDir())
	grant := seedJuniorAccess(t, pool, clubA, admin, "record-http-lead@example.invalid")
	server := &Server{DB: pool, LondonLoc: time.UTC}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	router.Mount("/junior", server.juniorPortalRouter())
	staffRequest := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		return adminWorkspaceHandlerRequest(t, router, admin, method, path, form)
	}
	contacts := juniorPortalTestRequest(t, router, grant, "GET", fmt.Sprintf("/junior/contacts?season_id=%d", season), nil)
	assertAdminWorkspaceHandlerHTML(t, contacts, []string{"Adult contacts and dated roles", "Upload a PNG or JPEG badge", "Coaches do not need portal accounts"}, nil)
	form := url.Values{"csrf_token": {"synthetic-csrf-token"}, "season_id": {fmt.Sprint(season)}, "club_id": {fmt.Sprint(clubB)}, "full_name": {"Club A Adult"}, "email": {"adult-a@example.invalid"}, "role": {"coach"}, "age_group": {"U9"}, "starts_on": {"2026-10-17"}}
	noCSRF := juniorPortalTestRequest(t, router, grant, "POST", "/junior/contacts", url.Values{"full_name": {"No CSRF"}})
	if noCSRF.Code != 403 {
		t.Fatalf("contact CSRF bypass: %d", noCSRF.Code)
	}
	saved := juniorPortalTestRequest(t, router, grant, "POST", "/junior/contacts", form)
	if saved.Code != 303 {
		t.Fatalf("club contact save: %d %s", saved.Code, saved.Body.String())
	}
	var actualClub int32
	if err := pool.QueryRow(ctx, `SELECT club_id FROM junior_contacts WHERE email='adult-a@example.invalid'`).Scan(&actualClub); err != nil || actualClub != clubA {
		t.Fatalf("posted club changed lead scope: %d %v", actualClub, err)
	}
	preview := staffRequest("GET", fmt.Sprintf("/admin/juniors/clubs/%d/preview?view=contacts&season_id=%d", clubA, season), nil)
	assertAdminWorkspaceHandlerHTML(t, preview, []string{"View as Club", "Club A Adult", "read-only", `name="view" value="contacts"`}, []string{"Save adult contact", "Save badge", "Add junior team"})
	multipartPost := func(path, field, filename string, data []byte, values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile(field, filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(data); err != nil {
			t.Fatal(err)
		}
		for key, list := range values {
			for _, value := range list {
				if err = writer.WriteField(key, value); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "https://synthetic.invalid"+path, &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "synthetic-csrf-token"})
		addAdminWorkspaceHandlerSession(t, request, admin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		return w
	}
	upload := multipartPost("/admin/juniors/roster", "roster", "synthetic.csv", []byte("club_name,contact_name,email,role,age_group,team_name\nSynthetic Junior Club A,Roster Adult,roster@example.invalid,coach,U13,U13 A\n"), url.Values{"csrf_token": {"synthetic-csrf-token"}, "season_id": {fmt.Sprint(season)}})
	if upload.Code != 303 {
		t.Fatalf("roster upload: %d %s", upload.Code, upload.Body.String())
	}
	path := upload.Header().Get("Location")
	if _, err := strconv.ParseInt(strings.TrimPrefix(path, "/admin/juniors/roster/"), 10, 64); err != nil {
		t.Fatal(err)
	}
	step1 := staffRequest("GET", path, nil)
	assertAdminWorkspaceHandlerHTML(t, step1, []string{"Step 1", "Exact name matches are suggestions only", "Confirm club mappings"}, nil)
	review := staffRequest("POST", path+"/review", url.Values{"csrf_token": {"synthetic-csrf-token"}, "row_1_club": {fmt.Sprint(clubA)}})
	if review.Code != 303 {
		t.Fatalf("roster mapping: %d %s", review.Code, review.Body.String())
	}
	step2 := staffRequest("GET", path, nil)
	assertAdminWorkspaceHandlerHTML(t, step2, []string{"Step 2", "Club A Adult", "Create the source adult contact", "Apply reviewed roster once"}, nil)
	apply := url.Values{"csrf_token": {"synthetic-csrf-token"}, "confirm": {"apply"}, "row_1_club": {fmt.Sprint(clubA)}, "row_1_create_contact": {"1"}, "row_1_create_team": {"1"}}
	applied := staffRequest("POST", path+"/apply", apply)
	if applied.Code != 303 {
		t.Fatalf("roster apply: %d %s", applied.Code, applied.Body.String())
	}
	receipt := staffRequest("GET", path, nil)
	assertAdminWorkspaceHandlerHTML(t, receipt, []string{"Applied once: 1 rows, 0 skipped, 1 clubs activated, 1 new adult contacts, 1 new junior teams, 1 new dated roles"}, []string{"Apply reviewed roster once"})
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewGray(image.Rect(0, 0, 5, 5))); err != nil {
		t.Fatal(err)
	}
	badgeUpload := multipartPost(fmt.Sprintf("/admin/juniors/clubs/%d/badge", clubA), "badge", "badge.png", imageBytes.Bytes(), url.Values{"csrf_token": {"synthetic-csrf-token"}, "season_id": {fmt.Sprint(season)}})
	if badgeUpload.Code != 303 {
		t.Fatalf("badge upload: %d %s", badgeUpload.Code, badgeUpload.Body.String())
	}
	var badgeID int64
	if err := pool.QueryRow(ctx, `SELECT revision_id FROM junior_club_badges WHERE club_id=$1`, clubA).Scan(&badgeID); err != nil {
		t.Fatal(err)
	}
	badgeRead := juniorPortalTestRequest(t, router, grant, "GET", fmt.Sprintf("/junior/badges/%d", badgeID), nil)
	if badgeRead.Code != 200 || badgeRead.Header().Get("Content-Type") != "image/png" || badgeRead.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("private badge read: %d %v", badgeRead.Code, badgeRead.Header())
	}
	badgePreview := staffRequest("GET", fmt.Sprintf("/admin/juniors/clubs/%d/preview/badges/%d", clubA, badgeID), nil)
	if badgePreview.Code != 200 {
		t.Fatalf("preview badge read: %d %s", badgePreview.Code, badgePreview.Body.String())
	}
}

func TestJuniorRecordsPreserveSeniorReportingRemindersAndCardCandidatesPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	admin, clubA, clubB := seedJuniorAccessAdminClub(t, ctx, pool)
	service := junior.NewService(pool)
	staff := junior.Principal{AdminID: admin}
	if _, err := pool.Exec(ctx, `INSERT INTO seasons(id,name,start_date,end_date) VALUES(90001,'Senior fixture boundary','2026-07-01','2026-07-07'); INSERT INTO weeks(id,season_id,week_number,start_date,end_date) VALUES(90001,90001,1,'2026-07-01','2026-07-07')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO teams(id,club_id,name,active,play_cricket_team_id) VALUES(90001,$1,'1st XI',true,'senior-a'),(90002,$2,'1st XI',true,'senior-b')`, clubA, clubB); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO captains(team_id,full_name,email,active_from) VALUES(90001,'Senior Captain A','captain-a@example.invalid','2026-01-01'),(90002,'Senior Captain B','captain-b@example.invalid','2026-01-01'); INSERT INTO league_fixtures(play_cricket_match_id,season_id,match_date,home_team_pc_id,away_team_pc_id,home_club_name,away_club_name) VALUES(90001,90001,'2026-07-04','senior-a','senior-b','Synthetic Junior Club A','Synthetic Junior Club B')`); err != nil {
		t.Fatal(err)
	}
	// The existing card report resolves each team's outstanding fixtures while
	// its result cursor is open; provide a second isolated-schema connection.
	config := pool.Config()
	config.MaxConns = 2
	readPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer readPool.Close()
	server := &Server{DB: &db.Pool{Pool: readPool}, LondonLoc: time.UTC}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 6)
	type snapshot struct {
		Progress reportProgress
		Cards    []weeklyCardReportRow
		Reminder string
		Counts   []int64
	}
	read := func() snapshot {
		t.Helper()
		var out snapshot
		var e error
		out.Progress, e = server.loadWeekReportProgress(ctx, competitionWeek{ID: 90001, SeasonID: 90001, StartDate: start, EndDate: end})
		if e != nil {
			t.Fatal(e)
		}
		out.Cards, e = server.weeklyCardReportRows(ctx, cardReportWeek{ID: 90001, SeasonID: 90001, StartDate: start, EndDate: end})
		if e != nil {
			t.Fatal(e)
		}
		w := httptest.NewRecorder()
		server.handleAdminReminderPreview().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/reminders/preview?date=2026-07-04", nil))
		if w.Code != 200 {
			t.Fatalf("reminder preview status%d", w.Code)
		}
		out.Reminder = w.Body.String()
		for _, table := range []string{"seasons", "weeks", "teams", "captains", "league_fixtures", "submissions", "report_exemptions", "sanctions", "sanction_cases"} {
			var count int64
			if e = pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); e != nil {
				t.Fatal(e)
			}
			out.Counts = append(out.Counts, count)
		}
		return out
	}
	before := read()
	if before.Progress.Expected != 2 || before.Progress.Missing != 2 || len(before.Cards) != 2 || !strings.Contains(before.Reminder, "captain-a@example.invalid") {
		t.Fatalf("nonempty senior baseline missing: %+v", before.Progress)
	}
	season := juniorRecordsSeason(t, ctx, service, staff, "Next junior season", "open")
	if err = service.SetParticipation(ctx, staff, clubA, season, "active", "Synthetic boundary test"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveTeam(ctx, staff, junior.TeamInput{ClubID: clubA, Name: "U11 A"}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveContact(ctx, staff, junior.ContactInput{ClubID: clubA, SeasonID: season, FullName: "Junior Adult", Email: "junior@example.invalid", Role: "coach", AgeGroup: "U11", StartsOn: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveClub(ctx, staff, junior.ClubInput{Name: "New junior-only club"}); err != nil {
		t.Fatal(err)
	}
	if err = service.SetParticipation(ctx, staff, clubA, season, "withdrawn", "Junior withdrawal"); err != nil {
		t.Fatal(err)
	}
	after := read()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("junior maintenance changed senior reporting, reminder eligibility, card candidates or tables: progress %v -> %v; counts %v -> %v", before.Progress, after.Progress, before.Counts, after.Counts)
	}
}

func TestJuniorStaffAccessOnlySuperAdminAndOnePermissionPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	admin, clubA, _ := seedJuniorAccessAdminClub(t, ctx, pool)
	service := junior.NewService(pool)
	var superID, target int32
	if err := pool.QueryRow(ctx, `INSERT INTO admin_users(username,password_hash,email,role) VALUES('records-super',$1,'super@example.invalid','super_admin') RETURNING id`, []byte("synthetic")).Scan(&superID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO admin_users(username,password_hash,email,role) VALUES('records-target',$1,'target@example.invalid','admin') RETURNING id`, []byte("synthetic")).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListStaffAccess(ctx, junior.Principal{AdminID: admin}); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("junior staff self-grant management: %v", err)
	}
	super := junior.Principal{AdminID: superID}
	if err := service.SetStaffAccess(ctx, super, target, true, ""); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("missing reason accepted: %v", err)
	}
	if err := service.SetStaffAccess(ctx, super, target, true, "Appointed junior administrator"); err != nil {
		t.Fatal(err)
	}
	if err := junior.Authorize(ctx, pool, junior.Principal{AdminID: target}, clubA, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,'sanctions_investigate')`, target); err != nil {
		t.Fatal(err)
	}
	if err := service.SetStaffAccess(ctx, super, target, false, "Junior role ended"); err != nil {
		t.Fatal(err)
	}
	if err := junior.Authorize(ctx, pool, junior.Principal{AdminID: target}, clubA, false); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("revoked junior grant effective: %v", err)
	}
	var other, events int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM admin_user_permissions WHERE admin_user_id=$1 AND permission='sanctions_investigate'),(SELECT count(*) FROM junior_events WHERE entity_type='staff_access' AND entity_id=$1)`, target).Scan(&other, &events); err != nil || other != 1 || events != 2 {
		t.Fatalf("other permissions/audit changed %d/%d: %v", other, events, err)
	}
	t.Setenv("SUPER_ADMIN_EMAILS", "configured@example.invalid")
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET email='configured@example.invalid' WHERE id=$1`, target); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListStaffAccess(ctx, junior.Principal{AdminID: target}); err != nil {
		t.Fatalf("existing effective super-admin policy ignored: %v", err)
	}
}
