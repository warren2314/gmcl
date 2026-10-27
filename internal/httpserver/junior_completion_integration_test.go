package httpserver

import (
	"cricket-ground-feedback/internal/junior"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestJuniorCompletionDirectoryAudienceAndInactiveHistoryPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-junior-completion-test-secret")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	svc := junior.NewService(pool)
	staff := seed.Admin
	today := time.Now().AddDate(0, 0, -1)
	contacts := []int64{}
	for i, club := range seed.Clubs {
		email := "shared@example.invalid"
		if i == 2 {
			email = "private-c@example.invalid"
		}
		id, err := svc.SaveContact(ctx, staff, junior.ContactInput{ClubID: club, SeasonID: seed.SeasonID, FullName: fmt.Sprintf("Directory coach %d", i+1), Email: email, Role: "coach", AgeGroup: "U13", TeamID: &seed.Teams[i], StartsOn: today})
		if err != nil {
			t.Fatal(err)
		}
		contacts = append(contacts, id)
	}
	comp, err := svc.CreateCompetition(ctx, staff, junior.Competition{SeasonID: seed.SeasonID, Name: "Directory U13", AgeGroup: "U13", Format: "once", StartDate: "2027-05-01", EndDate: "2027-05-31", DefaultStartTime: "18:00"})
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range seed.Teams[:2] {
		if err = svc.SaveCompetitionEntry(ctx, staff, comp, juniorCompetitionRevision(t, ctx, svc, staff, comp), team, true); err != nil {
			t.Fatal(err)
		}
	}
	directoryFilter := junior.AudienceFilter{SeasonID: seed.SeasonID, CompetitionID: comp, Role: "coach", AgeGroup: "U13"}
	directory, err := svc.ContactDirectory(ctx, staff, directoryFilter, "")
	if err != nil || len(directory) != 2 {
		t.Fatalf("competition-specific directory: %+v %v", directory, err)
	}
	searched, err := svc.ContactDirectory(ctx, staff, directoryFilter, "coach 2")
	if err != nil || len(searched) != 1 || searched[0].ID != contacts[1] {
		t.Fatalf("directory search: %+v %v", searched, err)
	}
	filter := junior.AudienceFilter{SeasonID: seed.SeasonID, Selection: "contacts", ContactIDs: []int64{contacts[1], contacts[0], contacts[0]}, ClubIDs: []int32{seed.Clubs[1], seed.Clubs[0]}}
	preview, err := svc.PreviewAudience(ctx, staff, filter)
	if err != nil || len(preview.Recipients) != 2 {
		t.Fatalf("explicit contacts: %+v %v", preview, err)
	}
	reordered := filter
	reordered.ContactIDs = []int64{contacts[0], contacts[1]}
	reordered.ClubIDs = []int32{seed.Clubs[0], seed.Clubs[1]}
	equivalent, err := svc.PreviewAudience(ctx, staff, reordered)
	if err != nil || equivalent.Hash != preview.Hash {
		t.Fatalf("selection order changed intended audience: %v", err)
	}
	message, err := svc.ComposeMessage(ctx, staff, filter, preview.Hash, "Selected clubs only", "Synthetic selection test", "completion:selected")
	if err != nil {
		t.Fatal(err)
	}
	var recipients, audiences int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM junior_message_recipients WHERE message_id=$1),(SELECT count(*) FROM junior_message_audiences WHERE message_id=$1)`, message).Scan(&recipients, &audiences); err != nil || recipients != 1 || audiences != 2 {
		t.Fatalf("selection dedup lost club attribution: %d/%d %v", recipients, audiences, err)
	}
	if _, err = svc.PreviewAudience(ctx, staff, junior.AudienceFilter{SeasonID: seed.SeasonID, Selection: "contacts"}); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("empty selection widened: %v", err)
	}
	restricted := filter
	restricted.ClubIDs = []int32{seed.Clubs[0]}
	if _, err = svc.PreviewAudience(ctx, staff, restricted); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("out-of-filter contact accepted: %v", err)
	}
	if err = svc.SetClubInactive(ctx, staff, seed.Clubs[1], true, "No junior programme this year"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ComposeMessage(ctx, staff, filter, preview.Hash, "Stale audience", "Must re-preview", "completion:stale"); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("inactive recipient accepted from stale preview: %v", err)
	}
	directory, err = svc.ContactDirectory(ctx, staff, directoryFilter, "")
	if err != nil || len(directory) != 2 || !directory[1].Inactive {
		t.Fatalf("inactive club disappeared from directory: %+v %v", directory, err)
	}
	dashboard, err := svc.Dashboard(ctx, staff, seed.SeasonID)
	if err != nil || dashboard.ActiveClubs != 2 {
		t.Fatalf("inactive readiness count: %+v %v", dashboard, err)
	}
	previous := juniorRecordsSeason(t, ctx, svc, staff, "Previous junior season", "closed")
	profile, err := svc.ClubProfile(ctx, staff, seed.Clubs[1], previous)
	if err != nil || !profile.Club.Inactive {
		t.Fatalf("status lost across seasons: %+v %v", profile, err)
	}
	profile, err = svc.ClubProfile(ctx, staff, seed.Clubs[1], seed.SeasonID)
	if err != nil || profile.Club.State != "active" || len(profile.Contacts) != 1 {
		t.Fatalf("inactive erased participation/contact history: %+v %v", profile, err)
	}
	if err = svc.SetClubInactive(ctx, staff, seed.Clubs[1], false, "Junior programme restarted"); err != nil {
		t.Fatal(err)
	}
	if err = svc.SetParticipation(ctx, staff, seed.Clubs[2], seed.SeasonID, "not_entered", "Entry declined"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PreviewAudience(ctx, staff, junior.AudienceFilter{SeasonID: seed.SeasonID, Selection: "contacts", ContactIDs: []int64{contacts[2]}}); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("non-entered contact selected: %v", err)
	}
	lead := seedJuniorAccess(t, pool, seed.Clubs[0], staff.AdminID, "completion-lead@example.invalid")
	if _, err = svc.ContactDirectory(ctx, lead.Principal, directoryFilter, ""); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("club accessed league directory: %v", err)
	}
	if err = svc.SetClubInactive(ctx, lead.Principal, seed.Clubs[0], true, "Club attempted staff action"); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("club changed league status: %v", err)
	}
	server := &Server{DB: pool, LondonLoc: juniorLondon()}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	for _, suffix := range []string{"", "&export=csv"} {
		response := adminWorkspaceHandlerRequest(t, router, staff.AdminID, "GET", fmt.Sprintf("/admin/juniors/contacts?season_id=%d&competition_id=%d%s", seed.SeasonID, comp, suffix), nil)
		if response.Code != 200 || !strings.Contains(response.Body.String(), "Directory coach 1") || strings.Contains(response.Body.String(), "private-c@example.invalid") {
			t.Fatalf("directory/CSV filter mismatch: %d %s", response.Code, response.Body.String())
		}
	}
	values := url.Values{"season_id": {strconv.FormatInt(seed.SeasonID, 10)}, "audience_mode": {"contacts"}, "contact_ids": {strconv.FormatInt(contacts[0], 10)}, "subject": {"One coach only"}, "body": {"Synthetic HTTP preview"}, "operation": {"preview"}}
	values.Set("csrf_token", "synthetic-csrf-token")
	response := adminWorkspaceHandlerRequest(t, router, staff.AdminID, "POST", "/admin/juniors/messages", values)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Audience preview: 1 unique email addresses") {
		t.Fatalf("HTTP selection preview: %d %s", response.Code, response.Body.String())
	}
}

func TestJuniorCompletionLinkedClubWorkspaceIsolationPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	svc := junior.NewService(pool)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-junior-completion-profile-secret")
	for i, club := range seed.Clubs[:2] {
		_, err := svc.PublishForm(ctx, seed.Admin, junior.FormInput{SeasonID: seed.SeasonID, Title: fmt.Sprintf("Private form for club %d", i), DueDate: time.Now().AddDate(0, 0, 7), ClubIDs: []int32{club}, Fields: []junior.FormField{{Key: "confirmed", Label: "Confirmed", Type: "yes_no", Required: true}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := svc.ClubWorkspace(ctx, seed.Admin, seed.Clubs[0], seed.SeasonID)
	if err != nil || len(data.Forms) != 1 || data.Forms[0].Title != "Private form for club 0" {
		t.Fatalf("staff profile form scope: %+v %v", data, err)
	}
	lead := seedJuniorAccess(t, pool, seed.Clubs[0], seed.Admin.AdminID, "workspace@example.invalid")
	if _, err = svc.ClubWorkspace(ctx, lead.Principal, seed.Clubs[1], seed.SeasonID); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("cross-club profile: %v", err)
	}
	server := &Server{DB: pool, LondonLoc: juniorLondon()}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	response := adminWorkspaceHandlerRequest(t, router, seed.Admin.AdminID, "GET", fmt.Sprintf("/admin/juniors/clubs/%d?season_id=%d", seed.Clubs[0], seed.SeasonID), nil)
	if response.Code != 200 {
		t.Fatalf("profile failed: %d %s", response.Code, response.Body.String())
	}
	for _, want := range []string{"Private form for club 0", "club-forms", "club-competitions", "club-fixtures", "club-concessions", "club-messages", "Junior club status"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("missing linked section %s", want)
		}
	}
	if strings.Contains(response.Body.String(), "Private form for club 1") {
		t.Fatal("other club form leaked into profile")
	}
}
