package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/junior"
	"github.com/go-chi/chi/v5"
)

func TestJuniorMessageSnapshotsScopeAndDispatchPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("JUNIOR_OUTBOUND_EMAIL_ENABLED", "false")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	adminID, clubA, clubB := seedJuniorAccessAdminClub(t, ctx, pool)
	staff := junior.Principal{AdminID: adminID}
	service := junior.NewService(pool)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seasonID, err := service.SaveSeason(ctx, staff, junior.SeasonInput{Name: "Synthetic communication season", StartDate: today.AddDate(0, -1, 0), EndDate: today.AddDate(0, 12, 0), State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	for _, clubID := range []int32{clubA, clubB} {
		if err := service.SetParticipation(ctx, staff, clubID, seasonID, "active", "synthetic test"); err != nil {
			t.Fatal(err)
		}
	}
	contactA, err := service.SaveContact(ctx, staff, junior.ContactInput{ClubID: clubA, SeasonID: seasonID, FullName: "Synthetic Club A Coach", Email: "shared@example.invalid", Role: "coach", AgeGroup: "U13", StartsOn: today})
	if err != nil {
		t.Fatal(err)
	}
	contactB, err := service.SaveContact(ctx, staff, junior.ContactInput{ClubID: clubB, SeasonID: seasonID, FullName: "Synthetic Club B Coach", Email: "shared@example.invalid", Role: "coach", AgeGroup: "U13", StartsOn: today})
	if err != nil {
		t.Fatal(err)
	}
	privateB, err := service.SaveContact(ctx, staff, junior.ContactInput{ClubID: clubB, SeasonID: seasonID, FullName: "Synthetic Private B", Email: "private-b@example.invalid", Role: "junior_lead", StartsOn: today})
	if err != nil {
		t.Fatal(err)
	}
	filter := junior.AudienceFilter{SeasonID: seasonID, Role: "coach", AgeGroup: "U13"}
	preview, err := service.PreviewAudience(ctx, staff, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Recipients) != 2 {
		t.Fatalf("coach audience: %+v", preview.Recipients)
	}
	messageID, err := service.ComposeMessage(ctx, staff, filter, preview.Hash, "Synthetic coach notice", "Shared synthetic body", "synthetic:coaches:1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.ComposeMessage(ctx, staff, filter, preview.Hash, "Synthetic coach notice", "Shared synthetic body", "synthetic:coaches:1")
	if err != nil || replay != messageID {
		t.Fatalf("queue deduplication: %d %v", replay, err)
	}
	var recipients, audiences int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM junior_message_recipients WHERE message_id=$1),(SELECT count(*) FROM junior_message_audiences WHERE message_id=$1)`, messageID).Scan(&recipients, &audiences); err != nil || recipients != 1 || audiences != 2 {
		t.Fatalf("shared mailbox dedup: recipients=%d audiences=%d err=%v", recipients, audiences, err)
	}
	if err := service.DispatchMessage(ctx, staff, messageID); !errors.Is(err, junior.ErrTransportUnavailable) {
		t.Fatalf("disabled SMTP dispatched: %v", err)
	}
	var sent []junior.MailEnvelope
	service.Delivery = func(_ context.Context, envelope junior.MailEnvelope) error {
		if !strings.HasSuffix(envelope.To, "@example.invalid") {
			t.Fatal("recipient escaped synthetic domain")
		}
		sent = append(sent, envelope)
		return nil
	}
	if err := service.DispatchMessage(ctx, staff, messageID); err != nil {
		t.Fatal(err)
	}
	if err := service.DispatchMessage(ctx, staff, messageID); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].To != "shared@example.invalid" || !strings.HasPrefix(sent[0].MessageID, "<junior-") {
		t.Fatalf("once per address dispatch: %+v", sent)
	}
	if strings.Contains(sent[0].Body, "private-b@example.invalid") {
		t.Fatal("another recipient address was embedded in message body")
	}
	leadA := seedJuniorAccess(t, pool, clubA, adminID, "lead-a@example.invalid")
	leadB := seedJuniorAccess(t, pool, clubB, adminID, "lead-b@example.invalid")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	privateID, err := junior.Enqueue(ctx, tx, staff, junior.MessageIntent{SeasonID: seasonID, ClubID: clubB, Kind: "staff_message", Key: "synthetic:private-b", Subject: "Private B notice", Body: "Private Club B content", Recipients: []junior.Recipient{{ClubID: clubB, ContactID: privateB, Email: "private-b@example.invalid", Name: "Private B"}}})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	own, err := service.Messages(ctx, leadA.Principal, clubA)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 || own[0].ID != messageID || own[0].Status != "accepted" || len(own[0].Recipients) != 1 || own[0].Recipients[0].ContactID != contactA {
		t.Fatalf("club A message scope: %+v", own)
	}
	other, err := service.Messages(ctx, leadB.Principal, clubB)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 2 {
		t.Fatalf("club B message scope: %+v", other)
	}
	for _, item := range other {
		if item.ID == messageID && (len(item.Recipients) != 1 || item.Recipients[0].ContactID != contactB) {
			t.Fatalf("club B leaked club A association: %+v", item)
		}
	}
	if _, err := service.Messages(ctx, leadA.Principal, clubB); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("cross-club message history: %v", err)
	}
	readonly := junior.Principal{AdminID: adminID, ClubID: clubA, ReadOnly: true}
	if err := service.DispatchMessage(ctx, readonly, messageID); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("preview dispatched: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE junior_messages SET body='changed' WHERE id=$1`, messageID); err == nil {
		t.Fatal("message snapshot changed")
	}
	if _, err := pool.Exec(ctx, `UPDATE junior_message_recipients SET email='changed@example.invalid' WHERE message_id=$1`, messageID); err == nil {
		t.Fatal("recipient snapshot changed")
	}
	service.Delivery = func(context.Context, junior.MailEnvelope) error { return errors.New("synthetic provider rejection") }
	if err := service.DispatchMessage(ctx, staff, privateID); !errors.Is(err, junior.ErrTransportUnavailable) {
		t.Fatalf("transport failure status: %v", err)
	}
	var failureStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM junior_message_recipients WHERE message_id=$1`, privateID).Scan(&failureStatus); err != nil || failureStatus != "failed" {
		t.Fatalf("failure not persisted: %s %v", failureStatus, err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gapID, err := junior.Enqueue(ctx, tx, staff, junior.MessageIntent{SeasonID: seasonID, ClubID: clubA, Kind: "fixture_publication", Key: "synthetic:gap", Subject: "Publication with missing contact", Body: "Official publication remains available in the portal"})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	history, err := service.Messages(ctx, staff, clubA)
	if err != nil {
		t.Fatal(err)
	}
	if history[0].ID != gapID || history[0].Status != "needs_recipients" {
		t.Fatalf("notification gap hidden: %+v", history)
	}
	own, err = service.Messages(ctx, leadA.Principal, clubA)
	if err != nil || len(own) != 1 {
		t.Fatalf("unaddressed gap leaked to club: %+v %v", own, err)
	}
	resolutionFilter := junior.AudienceFilter{SeasonID: seasonID, ClubID: clubA, Role: "coach", AgeGroup: "U13"}
	resolution, err := service.PreviewAudience(ctx, staff, resolutionFilter)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ResolveMessageRecipients(ctx, staff, gapID, resolutionFilter, resolution.Hash); err != nil {
		t.Fatal(err)
	}
	if err := service.ResolveMessageRecipients(ctx, staff, gapID, resolutionFilter, resolution.Hash); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("existing immutable audience replaced: %v", err)
	}
	// The original review becomes stale when the directory changes.
	if _, err := service.SaveContact(ctx, staff, junior.ContactInput{ID: contactA, ClubID: clubA, SeasonID: seasonID, FullName: "Synthetic Club A Coach", Email: "changed@example.invalid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ComposeMessage(ctx, staff, filter, preview.Hash, "Stale preview", "Must not queue", "synthetic:stale"); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("stale audience accepted: %v", err)
	}
	incomingID, err := service.RecordIncoming(ctx, staff, 0, "outside@example.invalid", "Synthetic inbound", "Manually copied, no provider connection")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := service.IncomingMessages(ctx, staff)
	if err != nil || len(inbox) != 1 || inbox[0].State != "unmatched" {
		t.Fatalf("unmatched incoming: %+v %v", inbox, err)
	}
	if err := service.LinkIncomingClub(ctx, staff, incomingID, clubA); err != nil {
		t.Fatal(err)
	}
	if _, err := service.IncomingMessages(ctx, leadA.Principal); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("club read staff incoming inbox: %v", err)
	}
	if _, err := service.RecordIncoming(ctx, readonly, clubA, "outside@example.invalid", "Denied", "Preview cannot record"); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("preview recorded inbound: %v", err)
	}
}

func TestJuniorMessagesHTTPPermissionsPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-junior-handler-secret")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("JUNIOR_OUTBOUND_EMAIL_ENABLED", "false")
	adminID, clubID, _ := seedJuniorAccessAdminClub(t, ctx, pool)
	server := &Server{DB: pool}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	response := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodGet, "/admin/juniors/messages", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Mailbox provider is not connected") || !strings.Contains(response.Body.String(), "Outbound junior email is disabled") {
		t.Fatalf("staff messaging page: %d %s", response.Code, response.Body.String())
	}
	unauthenticated := adminWorkspaceHandlerRequest(t, router, 0, http.MethodGet, "/admin/juniors/messages", nil)
	if unauthenticated.Code != 303 {
		t.Fatalf("anonymous inbox: %d", unauthenticated.Code)
	}
	badCSRF := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodPost, "/admin/juniors/messages", url.Values{"operation": {"record_incoming"}, "from": {"sender@example.invalid"}, "subject": {"Should not record"}, "body": {"CSRF denied"}})
	if badCSRF.Code != 403 {
		t.Fatalf("incoming CSRF bypass: %d", badCSRF.Code)
	}
	service := junior.NewService(pool)
	staff := junior.Principal{AdminID: adminID}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seasonID, err := service.SaveSeason(ctx, staff, junior.SeasonInput{Name: "Synthetic HTTP message season", StartDate: today.AddDate(0, -1, 0), EndDate: today.AddDate(0, 12, 0), State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetParticipation(ctx, staff, clubID, seasonID, "active", "synthetic HTTP fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveContact(ctx, staff, junior.ContactInput{ClubID: clubID, SeasonID: seasonID, FullName: "Synthetic HTTP Coach", Email: "http-coach@example.invalid", Role: "coach", AgeGroup: "U15", StartsOn: today}); err != nil {
		t.Fatal(err)
	}
	compose := url.Values{"csrf_token": {"synthetic-csrf-token"}, "operation": {"preview"}, "season_id": {strconv.FormatInt(seasonID, 10)}, "club_id": {strconv.FormatInt(int64(clubID), 10)}, "role": {"coach"}, "age_group": {"U15"}, "key": {"synthetic:http-compose"}, "subject": {"Synthetic reviewed HTTP notice"}, "body": {"Reviewed body, no real email"}}
	previewed := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodPost, "/admin/juniors/messages", compose)
	if previewed.Code != 200 || !strings.Contains(previewed.Body.String(), "Audience preview: 1 unique email addresses") {
		t.Fatalf("HTTP audience preview: %d %s", previewed.Code, previewed.Body.String())
	}
	hash := regexp.MustCompile(`name="audience_hash" value="([a-f0-9]{64})"`).FindStringSubmatch(previewed.Body.String())
	if len(hash) != 2 {
		t.Fatal("review did not expose an audience checkpoint")
	}
	compose.Set("operation", "queue")
	compose.Set("audience_hash", hash[1])
	queued := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodPost, "/admin/juniors/messages", compose)
	if queued.Code != 303 {
		t.Fatalf("HTTP queue command: %d %s", queued.Code, queued.Body.String())
	}
	var messageID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM junior_messages WHERE idempotency_key='synthetic:http-compose'`).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	dispatch := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodPost, "/admin/juniors/messages", url.Values{"csrf_token": {"synthetic-csrf-token"}, "operation": {"dispatch"}, "message_id": {strconv.FormatInt(messageID, 10)}})
	if dispatch.Code != 503 {
		t.Fatalf("disabled HTTP dispatch: %d %s", dispatch.Code, dispatch.Body.String())
	}
	problems := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodGet, fmt.Sprintf("/admin/juniors/messages?queue=problems&season_id=%d", seasonID), nil)
	if problems.Code != 200 || strings.Contains(problems.Body.String(), "Synthetic reviewed HTTP notice") {
		t.Fatalf("problem filter included queued message: %d %s", problems.Code, problems.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE junior_message_recipients SET status='failed' WHERE message_id=$1`, messageID); err != nil {
		t.Fatal(err)
	}
	problems = adminWorkspaceHandlerRequest(t, router, adminID, http.MethodGet, fmt.Sprintf("/admin/juniors/messages?queue=problems&season_id=%d", seasonID), nil)
	if problems.Code != 200 || !strings.Contains(problems.Body.String(), "Synthetic reviewed HTTP notice") {
		t.Fatalf("problem filter lost failed message: %d %s", problems.Code, problems.Body.String())
	}
	captureAdminWorkspaceHandlerHTML(t, "junior-messages-audience-preview", previewed.Body.String())
	captureAdminWorkspaceHandlerHTML(t, "junior-messages-problems", problems.Body.String())
	if _, err := pool.Exec(ctx, `DELETE FROM admin_user_permissions WHERE admin_user_id=$1`, adminID); err != nil {
		t.Fatal(err)
	}
	denied := adminWorkspaceHandlerRequest(t, router, adminID, http.MethodGet, "/admin/juniors/messages", nil)
	if denied.Code != 403 {
		t.Fatalf("ungranted staff inbox: %d", denied.Code)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM junior_incoming_messages`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unauthorised incoming mutation: count=%d err=%v", count, err)
	}
}
