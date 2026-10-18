package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func juniorAccessTokenFromBody(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`/junior/access\?token=([A-Za-z0-9_-]{43})`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("synthetic access email did not contain one usable link")
	}
	return match[1]
}

func TestJuniorAccessConcurrentRedemptionAndRevocationPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("JUNIOR_PORTAL_ENABLED", "true")
	t.Setenv("PUBLIC_BASE_URL", "https://junior.example.invalid")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	adminID, clubID, _ := seedJuniorAccessAdminClub(t, ctx, pool)
	staff := junior.Principal{AdminID: adminID}
	cfg := pool.Config().Copy()
	cfg.MaxConns = 4
	concurrentPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(concurrentPool.Close)
	service := junior.NewService(&db.Pool{Pool: concurrentPool})
	var body string
	service.AccessMailer = func(_ context.Context, _, _, value string) error { body = value; return nil }
	if err := service.InviteLead(ctx, staff, clubID, "concurrent@example.invalid"); err != nil {
		t.Fatal(err)
	}
	raw := juniorAccessTokenFromBody(t, body)
	type result struct {
		grant junior.SessionGrant
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for range 2 {
		go func() { <-start; grant, err := service.RedeemAccess(ctx, raw); results <- result{grant, err} }()
	}
	close(start)
	var grant junior.SessionGrant
	successes, denials := 0, 0
	for range 2 {
		select {
		case item := <-results:
			if item.err == nil {
				successes++
				grant = item.grant
			} else if errors.Is(item.err, junior.ErrForbidden) {
				denials++
			} else {
				t.Fatal(item.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent redemption did not complete")
		}
	}
	if successes != 1 || denials != 1 {
		t.Fatalf("single use under concurrent redemption: successes=%d denials=%d", successes, denials)
	}
	service.AccessMailer = func(context.Context, string, string, string) error { return nil }
	for range 3 {
		if _, err := pool.Exec(ctx, `UPDATE junior_access_tokens SET created_at=now()-interval '2 minutes' WHERE account_id=$1`, grant.Principal.AccountID); err != nil {
			t.Fatal(err)
		}
		simultaneous := make(chan error, 2)
		gate := make(chan struct{})
		go func() { <-gate; simultaneous <- service.RequestLogin(ctx, "concurrent@example.invalid") }()
		go func() { <-gate; simultaneous <- service.InviteLead(ctx, staff, clubID, "concurrent@example.invalid") }()
		close(gate)
		for range 2 {
			select {
			case err := <-simultaneous:
				if err != nil {
					t.Fatalf("simultaneous login and handover: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("simultaneous login and handover deadlocked")
			}
		}
	}
	var membershipID int64
	if err := pool.QueryRow(ctx, `SELECT membership_id FROM junior_sessions WHERE id=$1`, grant.Principal.SessionID).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	tx, err := concurrentPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := junior.Authorize(ctx, tx, grant.Principal, clubID, true); err != nil {
		t.Fatal(err)
	}
	revoked := make(chan error, 1)
	go func() { revoked <- service.RevokeLead(ctx, staff, clubID, membershipID) }()
	select {
	case err := <-revoked:
		t.Fatalf("revocation passed an authorised write transaction's locks: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := tx.Exec(ctx, `INSERT INTO junior_teams(club_id,name) VALUES($1,'Synthetic authorised before revocation')`, clubID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revocation remained blocked after write committed")
	}
	if _, err := service.SaveTeam(ctx, grant.Principal, junior.TeamInput{ClubID: clubID, Name: "Denied after revocation"}); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("write after revoke: %v", err)
	}
}

// This helper uses the real invitation/acceptance services and an injected fake
// mailer. The raw secret remains in test memory; no SMTP or workers are started.
func seedJuniorAccess(t *testing.T, pool *db.Pool, clubID, adminID int32, email string) junior.SessionGrant {
	t.Helper()
	t.Setenv("JUNIOR_PORTAL_ENABLED", "true")
	t.Setenv("PUBLIC_BASE_URL", "https://junior.example.invalid")
	service := junior.NewService(pool)
	var body string
	service.AccessMailer = func(_ context.Context, _, _, message string) error { body = message; return nil }
	if err := service.InviteLead(t.Context(), junior.Principal{AdminID: adminID}, clubID, email); err != nil {
		t.Fatal(err)
	}
	grant, err := service.RedeemAccess(t.Context(), juniorAccessTokenFromBody(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func seedJuniorAccessAdminClub(t *testing.T, ctx context.Context, pool *db.Pool) (int32, int32, int32) {
	t.Helper()
	var adminID, clubA, clubB int32
	if err := pool.QueryRow(ctx, `INSERT INTO admin_users(username,password_hash,email,role) VALUES('junior-access-fixture',$1,'staff@example.invalid','admin') RETURNING id`, []byte("synthetic-not-a-password")).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,'junior_manage')`, adminID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO clubs(name) VALUES('Synthetic Junior Club A') RETURNING id`).Scan(&clubA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO clubs(name) VALUES('Synthetic Junior Club B') RETURNING id`).Scan(&clubB); err != nil {
		t.Fatal(err)
	}
	return adminID, clubA, clubB
}

func TestJuniorLeadAccessLifecyclePostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("JUNIOR_PORTAL_ENABLED", "true")
	t.Setenv("PUBLIC_BASE_URL", "https://junior.example.invalid")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("JUNIOR_OUTBOUND_EMAIL_ENABLED", "false")
	adminID, clubA, clubB := seedJuniorAccessAdminClub(t, ctx, pool)
	staff := junior.Principal{AdminID: adminID}
	service := junior.NewService(pool)
	var bodies []string
	service.AccessMailer = func(_ context.Context, to, _, body string) error {
		if !strings.HasSuffix(to, "@example.invalid") {
			t.Fatal("fixture recipient escaped invented domain")
		}
		bodies = append(bodies, body)
		return nil
	}
	if err := service.InviteLead(ctx, staff, clubA, "first@example.invalid"); err != nil {
		t.Fatal(err)
	}
	firstToken := juniorAccessTokenFromBody(t, bodies[len(bodies)-1])
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM junior_access_tokens WHERE token_hash=$1`, firstToken).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("raw token persisted: count=%d err=%v", leaked, err)
	}
	first, err := service.RedeemAccess(ctx, firstToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RedeemAccess(ctx, firstToken); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("replayed invitation: %v", err)
	}
	if err := junior.Authorize(ctx, pool, first.Principal, clubA, false); err != nil {
		t.Fatal(err)
	}
	if err := junior.Authorize(ctx, pool, first.Principal, clubB, false); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("cross-club scope accepted: %v", err)
	}
	if err := junior.Authorize(ctx, pool, junior.Principal{AdminID: adminID, ClubID: clubA, ReadOnly: true}, clubA, true); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("preview write accepted: %v", err)
	}
	if err := service.InviteLead(ctx, staff, clubA, "replacement@example.invalid"); err != nil {
		t.Fatal(err)
	}
	nextToken := juniorAccessTokenFromBody(t, bodies[len(bodies)-1])
	if _, err := service.SessionPrincipal(ctx, first.Token); err != nil {
		t.Fatalf("pending handover revoked old lead: %v", err)
	}
	next, err := service.RedeemAccess(ctx, nextToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SessionPrincipal(ctx, first.Token); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("handover retained old session: %v", err)
	}
	if err := junior.Authorize(ctx, pool, first.Principal, clubA, false); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("cached revoked principal retained access: %v", err)
	}
	var membershipID int64
	if err := pool.QueryRow(ctx, `SELECT membership_id FROM junior_sessions WHERE id=$1`, next.Principal.SessionID).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeLead(ctx, staff, clubB, membershipID); !errors.Is(err, junior.ErrNotFound) {
		t.Fatalf("cross-club revoke: %v", err)
	}
	if err := service.RevokeLead(ctx, staff, clubA, membershipID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SessionPrincipal(ctx, next.Token); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("revocation retained session: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET is_active=false WHERE id=$1`, adminID); err != nil {
		t.Fatal(err)
	}
	if err := junior.Authorize(ctx, pool, staff, clubA, false); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("inactive staff authorised: %v", err)
	}
	var admins, accounts, events int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM admin_users WHERE email IN ('first@example.invalid','replacement@example.invalid')),(SELECT count(*) FROM junior_accounts),(SELECT count(*) FROM junior_events WHERE action IN ('accepted_invite','revoked'))`).Scan(&admins, &accounts, &events); err != nil {
		t.Fatal(err)
	}
	if admins != 0 || accounts != 2 || events < 3 {
		t.Fatalf("identity separation/audit: admins=%d accounts=%d events=%d", admins, accounts, events)
	}
}

func TestJuniorAccessHTTPAndDeliveryPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("JUNIOR_PORTAL_ENABLED", "true")
	t.Setenv("PUBLIC_BASE_URL", "https://junior.example.invalid")
	t.Setenv("JUNIOR_OUTBOUND_EMAIL_ENABLED", "false")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-junior-access-handler-secret")
	adminID, clubA, clubB := seedJuniorAccessAdminClub(t, ctx, pool)
	var body string
	mailer := junior.AccessMailer(func(_ context.Context, _, _, value string) error { body = value; return nil })
	server := &Server{DB: pool, JuniorAccessMailer: mailer}
	service := server.juniorAccessService()
	if err := service.InviteLead(ctx, junior.Principal{AdminID: adminID}, clubA, "lead@example.invalid"); err != nil {
		t.Fatal(err)
	}
	raw := juniorAccessTokenFromBody(t, body)
	router := chi.NewRouter()
	router.Use(middleware.CSRFMiddleware)
	router.Mount("/admin", server.adminRouter())
	router.Get("/junior/access", server.handleJuniorAccessGet())
	router.Post("/junior/access", server.handleJuniorAccessPost())
	router.Post("/junior/login", server.handleJuniorLoginPost())
	router.Post("/junior/logout", server.handleJuniorLogout())
	router.With(server.requireJuniorLead()).Get("/junior", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "club=%d", server.juniorPrincipal(r).ClubID)
	})
	request := func(method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		var encoded string
		if form != nil {
			encoded = form.Encode()
		}
		r := httptest.NewRequest(method, path, strings.NewReader(encoded))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "synthetic-junior-csrf"})
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	get := request(http.MethodGet, "/junior/access?token="+raw, nil, nil)
	if get.Code != http.StatusOK || get.Header().Get("Cache-Control") != "no-store" || get.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("access GET: %d headers=%v", get.Code, get.Header())
	}
	if strings.Contains(get.Body.String(), "<script") {
		t.Fatal("raw-token confirmation page loaded JavaScript")
	}
	denied := request(http.MethodPost, "/junior/access", url.Values{"token": {raw}}, nil)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("CSRF bypass: %d", denied.Code)
	}
	accepted := request(http.MethodPost, "/junior/access", url.Values{"token": {raw}, "csrf_token": {"synthetic-junior-csrf"}}, nil)
	if accepted.Code != http.StatusSeeOther || accepted.Header().Get("Location") != "/junior" {
		t.Fatalf("acceptance: %d %s", accepted.Code, accepted.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range accepted.Result().Cookies() {
		if cookie.Name == juniorSessionCookie {
			session = cookie
		}
	}
	if session == nil || !session.Secure || !session.HttpOnly || session.Path != "/junior" || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe/missing session cookie: %+v", session)
	}
	own := request(http.MethodGet, "/junior", nil, session)
	if own.Code != 200 || own.Body.String() != fmt.Sprintf("club=%d", clubA) {
		t.Fatalf("club session: %d %s", own.Code, own.Body.String())
	}
	unknown := request(http.MethodPost, "/junior/login", url.Values{"email": {"unknown@example.invalid"}, "csrf_token": {"synthetic-junior-csrf"}}, nil)
	known := request(http.MethodPost, "/junior/login", url.Values{"email": {"lead@example.invalid"}, "csrf_token": {"synthetic-junior-csrf"}}, nil)
	if unknown.Code != known.Code || unknown.Body.String() != known.Body.String() {
		t.Fatal("login response enumerated membership")
	}
	loginToken := juniorAccessTokenFromBody(t, body)
	if _, err := service.RedeemAccess(ctx, loginToken); err != nil {
		t.Fatalf("sign-in link after verified membership: %v", err)
	}
	signedOut := request(http.MethodPost, "/junior/logout", url.Values{"csrf_token": {"synthetic-junior-csrf"}}, session)
	if signedOut.Code != 303 {
		t.Fatalf("logout: %d", signedOut.Code)
	}
	if _, err := service.SessionPrincipal(ctx, session.Value); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("logout retained session: %v", err)
	}
	invitePath := fmt.Sprintf("/admin/juniors/clubs/%d/invite", clubB)
	noInviteCSRF := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodPost, invitePath, url.Values{"email": {"http-invited@example.invalid"}})
	if noInviteCSRF.Code != 403 {
		t.Fatalf("staff invitation CSRF bypass: %d", noInviteCSRF.Code)
	}
	invited := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodPost, invitePath, url.Values{"csrf_token": {"synthetic-csrf-token"}, "email": {"http-invited@example.invalid"}})
	if invited.Code != 303 {
		t.Fatalf("staff invitation command: %d %s", invited.Code, invited.Body.String())
	}
	staffInvitedGrant, err := service.RedeemAccess(ctx, juniorAccessTokenFromBody(t, body))
	if err != nil || staffInvitedGrant.Principal.ClubID != clubB {
		t.Fatalf("HTTP invitation bound wrong club: %v", err)
	}
	var membershipID int64
	if err := pool.QueryRow(ctx, `SELECT membership_id FROM junior_sessions WHERE id=$1`, staffInvitedGrant.Principal.SessionID).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	revoked := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodPost, fmt.Sprintf("/admin/juniors/clubs/%d/revoke", clubB), url.Values{"csrf_token": {"synthetic-csrf-token"}, "membership_id": {fmt.Sprint(membershipID)}})
	if revoked.Code != 303 {
		t.Fatalf("staff revocation command: %d %s", revoked.Code, revoked.Body.String())
	}
	if _, err := service.SessionPrincipal(ctx, staffInvitedGrant.Token); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("HTTP revocation retained club session: %v", err)
	}
	service.AccessMailer = func(context.Context, string, string, string) error { return errors.New("synthetic transport failure") }
	if err := service.InviteLead(ctx, junior.Principal{AdminID: adminID}, clubA, "failed@example.invalid"); !errors.Is(err, junior.ErrTransportUnavailable) {
		t.Fatalf("delivery failure reported success: %v", err)
	}
	var failed, active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE delivery_state='failed' AND revoked_at IS NOT NULL),count(*) FILTER(WHERE delivery_state='failed' AND revoked_at IS NULL) FROM junior_access_tokens`).Scan(&failed, &active); err != nil || failed != 1 || active != 0 {
		t.Fatalf("failed token remained usable: failed=%d active=%d err=%v", failed, active, err)
	}
	t.Setenv("JUNIOR_PORTAL_ENABLED", "false")
	disabled := request(http.MethodGet, "/junior", nil, session)
	if disabled.Code != 503 {
		t.Fatalf("disabled portal: %d", disabled.Code)
	}
}
