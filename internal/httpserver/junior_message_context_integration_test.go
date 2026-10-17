package httpserver

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/junior"
	"github.com/go-chi/chi/v5"
)

func TestJuniorMessageHandlersPreserveSeasonAndClubContextPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-message-context-secret")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("JUNIOR_OUTBOUND_EMAIL_ENABLED", "false")
	adminID, clubA, clubB := seedJuniorAccessAdminClub(t, ctx, pool)
	staff := junior.Principal{AdminID: adminID}
	service := junior.NewService(pool)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seasonA, err := service.SaveSeason(ctx, staff, junior.SeasonInput{Name: "Synthetic selected message season", StartDate: today.AddDate(0, -1, 0), EndDate: today.AddDate(0, 12, 0), State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	seasonB, err := service.SaveSeason(ctx, staff, junior.SeasonInput{Name: "Synthetic older message season", StartDate: today.AddDate(-1, -1, 0), EndDate: today.AddDate(0, -2, 0), State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	contactID, err := service.SaveContact(ctx, staff, junior.ContactInput{ClubID: clubA, SeasonID: seasonA, FullName: "Synthetic Message Contact", Email: "context@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		season       int64
		key, subject string
	}{{seasonA, "context:current", "Selected season outgoing"}, {seasonB, "context:older", "Older season outgoing"}} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = junior.Enqueue(ctx, tx, staff, junior.MessageIntent{SeasonID: item.season, ClubID: clubA, Kind: "staff_message", Key: item.key, Subject: item.subject, Body: "Synthetic private body for selected club", Recipients: []junior.Recipient{{ClubID: clubA, ContactID: contactID, Email: "context@example.invalid", Name: "Synthetic Message Contact"}}})
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.RecordIncoming(ctx, staff, clubA, "sender-a@example.invalid", "Club A staff-only incoming", "Synthetic club A manual message"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordIncoming(ctx, staff, clubB, "sender-b@example.invalid", "Club B staff-only incoming", "Synthetic club B manual message"); err != nil {
		t.Fatal(err)
	}
	grant := seedJuniorAccess(t, pool, clubA, adminID, "context-lead@example.invalid")
	server := &Server{DB: pool, LondonLoc: juniorLondon()}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	router.Mount("/junior", server.juniorPortalRouter())
	selected := juniorPortalTestRequest(t, router, grant, http.MethodGet, fmt.Sprintf("/junior/messages?season_id=%d", seasonA), nil)
	assertAdminWorkspaceHandlerHTML(t, selected, []string{"Selected season outgoing", "Synthetic selected message season"}, []string{"Older season outgoing", "Club A staff-only incoming", "Club B staff-only incoming", "Send queued recipients / retry failed"})
	older := juniorPortalTestRequest(t, router, grant, http.MethodGet, fmt.Sprintf("/junior/messages?season_id=%d", seasonB), nil)
	assertAdminWorkspaceHandlerHTML(t, older, []string{"Older season outgoing"}, []string{"Selected season outgoing"})
	invalid := juniorPortalTestRequest(t, router, grant, http.MethodGet, "/junior/messages?season_id=bad", nil)
	if invalid.Code != 400 {
		t.Fatalf("invalid message season: %d", invalid.Code)
	}
	unknown := juniorPortalTestRequest(t, router, grant, http.MethodGet, "/junior/messages?season_id=9223372036854775807", nil)
	if unknown.Code != 404 {
		t.Fatalf("unknown message season: %d", unknown.Code)
	}
	staffClub := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodGet, fmt.Sprintf("/admin/juniors/messages?club_id=%d&season_id=%d", clubA, seasonA), nil)
	assertAdminWorkspaceHandlerHTML(t, staffClub, []string{"Selected season outgoing", "Club A staff-only incoming", "Open whole-message delivery controls"}, []string{"Older season outgoing", "Club B staff-only incoming", "Send queued recipients / retry failed"})
	global := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodGet, fmt.Sprintf("/admin/juniors/messages?season_id=%d", seasonA), nil)
	assertAdminWorkspaceHandlerHTML(t, global, []string{"Selected season outgoing", "Club A staff-only incoming", "Club B staff-only incoming", "Send queued recipients / retry failed"}, []string{"Older season outgoing"})
	preview := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodGet, fmt.Sprintf("/admin/juniors/clubs/%d/preview?view=messages&season_id=%d", clubA, seasonB), nil)
	assertAdminWorkspaceHandlerHTML(t, preview, []string{"read-only staff preview", "Older season outgoing"}, []string{"Selected season outgoing", "Club A staff-only incoming", "Send queued recipients / retry failed"})
	if strings.Contains(preview.Body.String(), "action=\"/admin/juniors/messages\"") {
		t.Fatal("preview exposed staff messaging mutation")
	}
	captureAdminWorkspaceHandlerHTML(t, "junior-message-club-season", selected.Body.String())
	captureAdminWorkspaceHandlerHTML(t, "junior-message-staff-club-context", staffClub.Body.String())
}
