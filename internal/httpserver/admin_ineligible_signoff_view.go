package httpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

// adminIsFinalSignOffAdmin reports whether this administrator is the one who
// gives the ineligible-player final sign-off. It reuses the same test that
// gates the sign-off button itself - an active admin whose email matches the
// active Play-Cricket recipient - so the queue shown can never disagree with
// the outcomes the account is allowed to issue. No name is hard-coded: change
// the Play-Cricket entry under Notice recipients and the queue follows.
func (s *Server) adminIsFinalSignOffAdmin(ctx context.Context, adminID int32) bool {
	return s.isActiveSanctionRecipientAdmin(ctx, adminID, "play_cricket")
}

// ineligibleSignOffViewRequested reports whether the focused sign-off page
// should be used. It is the default for the sign-off administrator; view=all
// opens the full queue so nothing is permanently out of reach.
func ineligibleSignOffViewRequested(values url.Values) bool {
	return strings.TrimSpace(values.Get("view")) != "all"
}

// ineligibleFullQueueRequested reports whether the full queue was asked for
// explicitly, so the page can offer a way back to the focused sign-off view.
func ineligibleFullQueueRequested(values url.Values) bool {
	return !ineligibleSignOffViewRequested(values)
}

type ineligibleSignOffCase struct {
	ID           int64
	Reference    string
	Player       string
	Club         string
	Team         string
	ApprovedAt   time.Time
	Effects      []string
	LeaguePoints bool
}

type ineligibleSignOffTask struct {
	ID         int64
	CaseID     int64
	Reference  string
	Note       string
	Status     string
	DueAt      *time.Time
	Unassigned bool
}

func (s *Server) loadIneligibleSignOffCases(ctx context.Context) ([]ineligibleSignOffCase, error) {
	rows, err := s.DB.Query(ctx, `SELECT cases.id,cases.reference,COALESCE(cases.player_name,''),
		COALESCE(club.name,''),COALESCE(team.name,''),cases.updated_at,
		COALESCE(ARRAY(SELECT DISTINCT effect.effect_type
		               FROM sanction_effect_revisions effect
		               WHERE effect.decision_revision_id=approved_decision.id
		               ORDER BY effect.effect_type),ARRAY[]::text[])
		FROM sanction_cases cases
		LEFT JOIN clubs club ON club.id=cases.club_id
		LEFT JOIN teams team ON team.id=cases.team_id
		LEFT JOIN LATERAL (
			SELECT decision.id FROM sanction_decision_revisions decision
			WHERE decision.case_id=cases.id AND decision.status='approved'
			ORDER BY decision.revision DESC,decision.id DESC LIMIT 1
		) approved_decision ON TRUE
		WHERE NOT cases.is_test
		  AND NOT EXISTS(SELECT 1 FROM sanction_case_events training WHERE training.case_id=cases.id AND training.event_type='case_training_designated')
		  AND cases.source_type='ineligible_player' AND cases.status='approved'
		ORDER BY cases.updated_at,cases.id LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cases := []ineligibleSignOffCase{}
	for rows.Next() {
		var item ineligibleSignOffCase
		if err = rows.Scan(&item.ID, &item.Reference, &item.Player, &item.Club, &item.Team, &item.ApprovedAt, &item.Effects); err != nil {
			return nil, err
		}
		for _, effect := range item.Effects {
			if effect == "points_adjustment" {
				item.LeaguePoints = true
			}
		}
		cases = append(cases, item)
	}
	return cases, rows.Err()
}

func (s *Server) loadIneligibleSignOffTasks(ctx context.Context, adminID int32) ([]ineligibleSignOffTask, error) {
	rows, err := s.DB.Query(ctx, `SELECT task.id,cases.id,cases.reference,COALESCE(task.current_note,''),
		task.status,task.due_at,task.assigned_admin_id IS NULL
		FROM sanction_follow_up_tasks task
		JOIN sanction_cases cases ON cases.id=task.case_id
		WHERE task.task_type='play_cricket_points' AND task.status IN ('open','in_progress')
		  AND NOT cases.is_test
		  AND NOT EXISTS(SELECT 1 FROM sanction_case_events training WHERE training.case_id=cases.id AND training.event_type='case_training_designated')
		  AND (task.assigned_admin_id=$1 OR task.assigned_admin_id IS NULL)
		ORDER BY task.due_at NULLS LAST,task.id LIMIT 100`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []ineligibleSignOffTask{}
	for rows.Next() {
		var item ineligibleSignOffTask
		if err = rows.Scan(&item.ID, &item.CaseID, &item.Reference, &item.Note, &item.Status, &item.DueAt, &item.Unassigned); err != nil {
			return nil, err
		}
		tasks = append(tasks, item)
	}
	return tasks, rows.Err()
}

func (s *Server) writeAdminIneligibleSignOffPage(w http.ResponseWriter, r *http.Request, adminID int32, adminName string) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	cases, err := s.loadIneligibleSignOffCases(ctx)
	if err != nil {
		slog.Error("load ineligible final sign-off queue", "admin_id", adminID, "error", err)
		http.Error(w, "could not load the sign-off queue", http.StatusInternalServerError)
		return
	}
	tasks, err := s.loadIneligibleSignOffTasks(ctx, adminID)
	if err != nil {
		slog.Error("load ineligible league-points tasks", "admin_id", adminID, "error", err)
		http.Error(w, "could not load the league-points tasks", http.StatusInternalServerError)
		return
	}
	canIssue := s.adminHasPermission(ctx, adminID, "sanctions_publish")
	csrf := middleware.CSRFToken(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageHead(w, "Outcomes to sign off")
	writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
	writeIneligibleFlash(w, r)
	writeIneligibleSignOffView(w, adminName, cases, tasks, canIssue, s.LondonLoc, time.Now())
	pageFooter(w)
}

func writeIneligibleSignOffView(w io.Writer, adminName string, cases []ineligibleSignOffCase, tasks []ineligibleSignOffTask, canIssue bool, loc *time.Location, now time.Time) {
	if loc == nil {
		loc = time.Local
	}
	name := strings.TrimSpace(adminName)
	if name == "" {
		name = "administrator"
	}
	fmt.Fprintf(w, `<main class="container py-4" style="max-width:900px"><div class="d-flex flex-column flex-lg-row justify-content-between gap-3 mb-4"><div><h1 class="h2 mb-1">Outcomes to sign off</h1><p class="text-muted mb-0">%s, this is your ineligible-player work. Each case below has an approved, locked decision and is waiting for your final sign-off before anything is sent.</p></div></div>`, escapeHTML(greetingFor(now.In(loc))+", "+name))

	if !canIssue && len(cases) > 0 {
		fmt.Fprint(w, `<div class="alert alert-warning"><strong>Sign-off access missing.</strong> These outcomes are waiting for you, but this account does not currently hold the permission to issue them. Ask a super administrator to check its <strong>Sanctions access</strong> before the response deadlines pass.</div>`)
	}
	fmt.Fprintf(w, `<section class="card shadow-sm mb-4 border-danger" id="sign-off"><div class="card-header d-flex flex-wrap justify-content-between align-items-center gap-2"><strong>Ready for your final sign-off</strong><span class="badge %s">%d waiting</span></div><div class="list-group list-group-flush">`,
		map[bool]string{true: "text-bg-danger", false: "text-bg-success"}[len(cases) > 0], len(cases))
	if len(cases) == 0 {
		fmt.Fprint(w, `<div class="list-group-item text-muted py-4">Nothing is waiting for your sign-off. Cases appear here once an approved decision has been locked.</div>`)
	}
	for _, item := range cases {
		effects := ineligibleSignOffEffectSummary(item.Effects)
		pointsNote := ""
		if item.LeaguePoints {
			pointsNote = `<div class="small text-warning-emphasis mt-1">After sign-off this one also needs its league points applying in Play-Cricket.</div>`
		}
		fmt.Fprintf(w, `<a class="list-group-item list-group-item-action" href="/admin/cases/%d"><div class="d-flex flex-wrap justify-content-between gap-2"><strong>%s</strong><span class="badge text-bg-danger">Sign off and issue</span></div><div>%s</div><div class="small text-muted">%s &middot; approved %s</div><div class="small">%s</div>%s</a>`,
			item.ID, escapeHTML(item.Reference),
			escapeHTML(defaultString(item.Player, "Player not recorded")),
			escapeHTML(defaultString(item.Club, item.Team)),
			escapeHTML(item.ApprovedAt.In(loc).Format("02 Jan 2006 15:04")),
			escapeHTML(effects), pointsNote)
	}
	fmt.Fprint(w, `</div></section>`)

	fmt.Fprintf(w, `<section class="card shadow-sm mb-4" id="league-points"><div class="card-header d-flex flex-wrap justify-content-between align-items-center gap-2"><strong>League points to apply in Play-Cricket</strong><span class="badge %s">%d open</span></div><div class="list-group list-group-flush">`,
		map[bool]string{true: "text-bg-warning", false: "text-bg-secondary"}[len(tasks) > 0], len(tasks))
	if len(tasks) == 0 {
		fmt.Fprint(w, `<div class="list-group-item text-muted py-4">No league-table adjustments are waiting to be keyed into Play-Cricket.</div>`)
	}
	for _, item := range tasks {
		due := "No due date"
		badge := "text-bg-secondary"
		if item.DueAt != nil {
			due = "Due " + item.DueAt.In(loc).Format("02 Jan 2006")
			if item.DueAt.Before(now) {
				due = "Overdue &middot; " + item.DueAt.In(loc).Format("02 Jan 2006")
				badge = "text-bg-danger"
			}
		}
		unassigned := ""
		if item.Unassigned {
			unassigned = `<div class="small text-danger mt-1">This task has no owner, so it cannot be completed yet. Ask a super administrator to check the active Play-Cricket entry under Notice recipients.</div>`
		}
		fmt.Fprintf(w, `<a class="list-group-item list-group-item-action" href="/admin/cases/tasks#task-%d"><div class="d-flex flex-wrap justify-content-between gap-2"><strong>%s</strong><span class="badge %s">%s</span></div><div class="small">%s</div>%s</a>`,
			item.ID, escapeHTML(item.Reference), badge, due,
			escapeHTML(defaultString(item.Note, "Apply the approved league-table adjustment")), unassigned)
	}
	fmt.Fprint(w, `</div></section>`)

	fmt.Fprint(w, `<div class="alert alert-light border d-flex flex-wrap justify-content-between align-items-center gap-2 mb-0"><div class="small text-muted">Report triage, imports and the rest of the ineligible-player queue are handled by the investigators and are hidden from this page.</div><a class="btn btn-sm btn-outline-secondary" href="/admin/ineligible?view=all">Show the full queue</a></div></main>`)
}

func ineligibleSignOffEffectSummary(effects []string) string {
	if len(effects) == 0 {
		return "Approved outcome"
	}
	labels := make([]string, 0, len(effects))
	for _, effect := range effects {
		label := effectLabel(effect)
		if label == "" {
			label = strings.ReplaceAll(effect, "_", " ")
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, ", ")
}

func greetingFor(localNow time.Time) string {
	switch {
	case localNow.Hour() < 12:
		return "Good morning"
	case localNow.Hour() < 18:
		return "Good afternoon"
	default:
		return "Good evening"
	}
}
