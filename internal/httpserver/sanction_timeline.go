package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

type sanctionTimelineRecord struct {
	ID                                                       int64
	Club, Team, Colour, Reason, Notes, Status, RuleReference string
	Week                                                     int
	WeekStart, WeekEnd                                       time.Time
	OffenceDate                                              *time.Time
	IssuedAt                                                 time.Time
	RemedyDue, AppealDue, ServedAt                           *time.Time
	Points                                                   *int32
	RuleReleaseID                                            *int64
}

func (s *Server) loadSanctionTimeline(ctx context.Context, id int64) (sanctionTimelineRecord, error) {
	var v sanctionTimelineRecord
	err := s.DB.QueryRow(ctx, `SELECT sa.id,cl.name,t.name,sa.colour,sa.reason,COALESCE(sa.notes,''),sa.status,w.week_number,w.start_date,w.end_date,
		sa.offence_date,sa.issued_at,sa.remedy_due_at,sa.appeal_due_at,sa.served_at,sa.points_deduction,sa.rule_release_id,COALESCE(sa.rule_reference,'')
		FROM sanctions sa JOIN clubs cl ON cl.id=sa.club_id JOIN teams t ON t.id=sa.team_id JOIN weeks w ON w.id=sa.week_id WHERE sa.id=$1`, id).Scan(
		&v.ID, &v.Club, &v.Team, &v.Colour, &v.Reason, &v.Notes, &v.Status, &v.Week, &v.WeekStart, &v.WeekEnd, &v.OffenceDate, &v.IssuedAt, &v.RemedyDue, &v.AppealDue, &v.ServedAt, &v.Points, &v.RuleReleaseID, &v.RuleReference)
	return v, err
}

func (s *Server) handleAdminSanctionTimelineGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		v, err := s.loadSanctionTimeline(ctx, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		type release struct {
			ID    int64
			Label string
		}
		rows, _ := s.DB.Query(ctx, `SELECT id,'Snapshot '||id||' — '||COALESCE(to_char(published_at,'DD Mon YYYY'),'not published') FROM rule_releases WHERE status IN ('active','archived') ORDER BY id DESC`)
		var releases []release
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var x release
				if rows.Scan(&x.ID, &x.Label) == nil {
					releases = append(releases, x)
				}
			}
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanction details")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<div class="container" style="max-width:900px"><div class="d-flex justify-content-between mb-3"><div><h2>%s — %s</h2><p class="text-muted">%s card, week %d (%s–%s)</p></div><a href="/admin/sanctions" class="btn btn-outline-secondary align-self-start">Back</a></div>
<form method="POST" action="/admin/sanctions/%d/timeline"><input type="hidden" name="csrf_token" value="%s"><div class="card mb-4"><div class="card-header fw-semibold">Recorded facts and applicable rule</div><div class="card-body"><div class="row g-3">
<div class="col-md-4"><label class="form-label">Offence date *</label><input class="form-control" type="date" name="offence_date" value="%s" required></div>
<div class="col-md-4"><label class="form-label">Remedy deadline</label><input class="form-control" type="datetime-local" name="remedy_due_at" value="%s"></div>
<div class="col-md-4"><label class="form-label">Appeal deadline</label><input class="form-control" type="datetime-local" name="appeal_due_at" value="%s"></div>
<div class="col-md-4"><label class="form-label">Served/resolved date</label><input class="form-control" type="datetime-local" name="served_at" value="%s"></div>
<div class="col-md-4"><label class="form-label">Rule reference *</label><input class="form-control" name="rule_reference" value="%s" placeholder="e.g. 8.1.3" required></div>
<div class="col-md-4"><label class="form-label">Historical rules snapshot *</label><select class="form-select" name="rule_release_id" required><option value="">Choose…</option>`, escapeHTML(v.Club), escapeHTML(v.Team), escapeHTML(v.Colour), v.Week, v.WeekStart.Format("02 Jan"), v.WeekEnd.Format("02 Jan 2006"), v.ID, escapeHTML(csrf), dateValue(v.OffenceDate), dateTimeValue(v.RemedyDue), dateTimeValue(v.AppealDue), dateTimeValue(v.ServedAt), escapeHTML(v.RuleReference))
		for _, rel := range releases {
			selected := ""
			if v.RuleReleaseID != nil && *v.RuleReleaseID == rel.ID {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, rel.ID, selected, escapeHTML(rel.Label))
		}
		fmt.Fprintf(w, `</select></div><div class="col-12"><label class="form-label">Recorded reason *</label><input class="form-control" name="reason" value="%s" required></div><div class="col-12"><label class="form-label">Notes</label><textarea class="form-control" name="notes" rows="4">%s</textarea></div><div class="col-12"><label class="form-label">Reason for this correction *</label><textarea class="form-control" name="correction_reason" rows="2" required></textarea><div class="form-text">The previous and replacement values, your identity, time and request ID are retained permanently.</div></div></div></div><div class="card-footer"><button class="btn btn-primary">Record immutable correction</button></div></div></form>`, escapeHTML(v.Reason), escapeHTML(v.Notes))
		rows2, _ := s.DB.Query(ctx, `SELECT event_type,event_at,COALESCE(notes,'') FROM sanction_events WHERE sanction_id=$1 ORDER BY event_at`, id)
		fmt.Fprint(w, `<div class="card"><div class="card-header fw-semibold">Audit timeline</div><ul class="list-group list-group-flush">`)
		if rows2 != nil {
			defer rows2.Close()
			for rows2.Next() {
				var typ, note string
				var at time.Time
				if rows2.Scan(&typ, &at, &note) == nil {
					fmt.Fprintf(w, `<li class="list-group-item"><strong>%s</strong> — %s<div class="text-muted small">%s</div></li>`, escapeHTML(strings.ReplaceAll(typ, "_", " ")), at.In(s.LondonLoc).Format("02 Jan 2006 15:04"), escapeHTML(note))
				}
			}
		}
		fmt.Fprint(w, `</ul></div></div>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminSanctionTimelinePost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if id < 1 || r.ParseForm() != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		offence, err := time.Parse("2006-01-02", r.FormValue("offence_date"))
		if err != nil {
			http.Error(w, "offence date is required", 400)
			return
		}
		releaseID, err := strconv.ParseInt(r.FormValue("rule_release_id"), 10, 64)
		if err != nil || releaseID < 1 {
			http.Error(w, "rules snapshot is required", 400)
			return
		}
		ref := strings.TrimSpace(r.FormValue("rule_reference"))
		reason := strings.TrimSpace(r.FormValue("reason"))
		correctionReason := strings.TrimSpace(r.FormValue("correction_reason"))
		if ref == "" || reason == "" || correctionReason == "" {
			http.Error(w, "rule reference, recorded reason and correction reason are required", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			http.Error(w, "save failed", 500)
			return
		}
		defer tx.Rollback(ctx)
		var beforeData []byte
		if err = tx.QueryRow(ctx, `SELECT jsonb_build_object('offence_date',offence_date,'remedy_due_at',remedy_due_at,'appeal_due_at',appeal_due_at,'served_at',served_at,'rule_release_id',rule_release_id,'rule_reference',rule_reference,'reason',reason,'notes',notes) FROM sanctions WHERE id=$1 FOR UPDATE`, id).Scan(&beforeData); err != nil {
			http.NotFound(w, r)
			return
		}
		remedyDue := parseLocalDateTime(r.FormValue("remedy_due_at"), s.LondonLoc)
		appealDue := parseLocalDateTime(r.FormValue("appeal_due_at"), s.LondonLoc)
		servedAt := parseLocalDateTime(r.FormValue("served_at"), s.LondonLoc)
		notes := strings.TrimSpace(r.FormValue("notes"))
		_, err = tx.Exec(ctx, `UPDATE sanctions SET offence_date=$2,remedy_due_at=$3,appeal_due_at=$4,served_at=$5,rule_release_id=$6,rule_reference=$7,reason=$8,notes=$9 WHERE id=$1`, id, offence, remedyDue, appealDue, servedAt, releaseID, ref, reason, notes)
		if err != nil {
			http.Error(w, "save failed", 500)
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO sanction_events(sanction_id,event_type,event_at,notes,created_by_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'details_corrected',now(),$2,$3,$4,$5,$6::jsonb,jsonb_build_object('offence_date',$7::date,'remedy_due_at',$8::timestamptz,'appeal_due_at',$9::timestamptz,'served_at',$10::timestamptz,'rule_release_id',$11::bigint,'rule_reference',$12::text,'reason',$13::text,'notes',$14::text),$15)`, id, "Rule "+ref+" and dates corrected", *actor.ID, actor.Label, correctionReason, string(beforeData), offence, remedyDue, appealDue, servedAt, releaseID, ref, reason, notes, actor.RequestID)
		if err != nil || tx.Commit(ctx) != nil {
			http.Error(w, "save failed", 500)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/sanctions/%d/timeline", id), http.StatusSeeOther)
	}
}

func (s *Server) handleCaptainDiscipline() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := getCaptainSessionFromRequest(r)
		if err != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if !s.captainSessionStillActive(ctx, sess) {
			http.Error(w, "captain session is no longer active", http.StatusForbidden)
			return
		}
		type row struct {
			ID                                 int64
			Colour, Reason, Notes, Status, Ref string
			Week                               int
			Start, End, Issued                 time.Time
			Offence, Remedy, Appeal, Served    *time.Time
			Points                             *int32
			URL                                string
		}
		rows, err := s.DB.Query(ctx, `SELECT sa.id,sa.colour,sa.reason,COALESCE(sa.notes,''),sa.status,COALESCE(sa.rule_reference,''),w.week_number,w.start_date,w.end_date,sa.issued_at,sa.offence_date,sa.remedy_due_at,sa.appeal_due_at,sa.served_at,sa.points_deduction,COALESCE((SELECT d.canonical_url FROM rule_documents d JOIN rule_chunks c ON c.document_id=d.id WHERE c.release_id=sa.rule_release_id AND (c.rule_reference=sa.rule_reference OR c.rule_reference LIKE sa.rule_reference||'.%') ORDER BY c.ordinal LIMIT 1),'') FROM sanctions sa JOIN weeks w ON w.id=sa.week_id WHERE sa.team_id=$1 AND sa.season_id=$2 AND sa.case_id IS NULL ORDER BY sa.issued_at DESC`, sess.TeamID, sess.SeasonID)
		if err != nil {
			http.Error(w, "could not load sanctions", 500)
			return
		}
		defer rows.Close()
		var items []row
		for rows.Next() {
			var x row
			if rows.Scan(&x.ID, &x.Colour, &x.Reason, &x.Notes, &x.Status, &x.Ref, &x.Week, &x.Start, &x.End, &x.Issued, &x.Offence, &x.Remedy, &x.Appeal, &x.Served, &x.Points, &x.URL) == nil {
				items = append(items, x)
			}
		}
		type caseRow struct {
			Reference, Reason, Status, Effect, Rule string
			Points                                  *int
			Starts, Ends                            *time.Time
		}
		var caseItems []caseRow
		caseRows, caseErr := s.DB.Query(ctx, `SELECT c.reference,c.public_summary,c.public_status,e.effect_type,d.rule_reference,e.points,e.starts_at,e.ends_at FROM sanction_cases c JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='approved' JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id WHERE c.team_id=$1 AND c.status IN ('approved','published','appealed','closed') AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=e.id) ORDER BY COALESCE(e.starts_at,c.approved_at) DESC`, sess.TeamID)
		if caseErr == nil {
			defer caseRows.Close()
			for caseRows.Next() {
				var x caseRow
				if caseRows.Scan(&x.Reference, &x.Reason, &x.Status, &x.Effect, &x.Rule, &x.Points, &x.Starts, &x.Ends) == nil {
					caseItems = append(caseItems, x)
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "My team sanctions")
		writeCaptainNav(w)
		fmt.Fprint(w, `<div class="container py-4" style="max-width:900px"><div class="d-flex justify-content-between align-items-center mb-4"><div><h2>My team sanctions</h2><p class="text-muted mb-0">Recorded cards, reasons, dates, and applicable rules for your team.</p></div><a class="btn btn-outline-secondary" href="/captain/form">Back to report</a></div>`)
		if len(items) == 0 && len(caseItems) == 0 {
			fmt.Fprint(w, `<div class="alert alert-success">There are no sanctions recorded for your team in this season.</div>`)
		}
		for _, x := range caseItems {
			badge := "secondary"
			if x.Effect == "yellow_card" {
				badge = "warning text-dark"
			}
			if x.Effect == "red_card" || x.Effect == "suspended_red" {
				badge = "danger"
			}
			fmt.Fprintf(w, `<article class="card mb-3"><div class="card-header d-flex justify-content-between"><strong>%s</strong><span class="badge bg-%s">%s · %s</span></div><div class="card-body"><h3 class="h5">Why this was recorded</h3><p>%s</p><dl class="row small">`, escapeHTML(x.Reference), badge, escapeHTML(effectLabel(x.Effect)), escapeHTML(x.Status), escapeHTML(x.Reason))
			if x.Starts != nil {
				fmt.Fprintf(w, `<dt class="col-sm-4">Effective</dt><dd class="col-sm-8">%s</dd>`, x.Starts.In(s.LondonLoc).Format("02 January 2006"))
			}
			if x.Ends != nil {
				fmt.Fprintf(w, `<dt class="col-sm-4">Ends</dt><dd class="col-sm-8">%s</dd>`, x.Ends.In(s.LondonLoc).Format("02 January 2006"))
			}
			if x.Points != nil {
				fmt.Fprintf(w, `<dt class="col-sm-4">Points deduction</dt><dd class="col-sm-8">%d</dd>`, *x.Points)
			}
			if x.Rule != "" {
				fmt.Fprintf(w, `<dt class="col-sm-4">Applicable rule</dt><dd class="col-sm-8">%s</dd>`, escapeHTML(x.Rule))
			}
			fmt.Fprint(w, `</dl><p class="small text-muted mb-0">Private evidence, correspondence and internal notes are excluded. Quote the case reference when responding or appealing.</p></div></article>`)
		}
		for _, x := range items {
			badge := "warning text-dark"
			if x.Colour == "red" {
				badge = "danger"
			}
			fmt.Fprintf(w, `<article class="card mb-3"><div class="card-header d-flex justify-content-between"><strong>Week %d — %s</strong><span class="badge bg-%s">%s card · %s</span></div><div class="card-body"><h3 class="h5">Why this was recorded</h3><p>%s</p>`, x.Week, x.Start.Format("02 Jan 2006"), badge, escapeHTML(x.Colour), escapeHTML(x.Status), escapeHTML(humanSanctionReason(x.Reason, x.Notes)))
			fmt.Fprint(w, `<dl class="row small">`)
			fact := func(label, value string) {
				if value != "" {
					fmt.Fprintf(w, `<dt class="col-sm-4">%s</dt><dd class="col-sm-8">%s</dd>`, label, escapeHTML(value))
				}
			}
			fact("Match week", x.Start.Format("02 January 2006")+" to "+x.End.Format("02 January 2006"))
			fact("Offence/deadline date", formatDate(x.Offence, s.LondonLoc))
			fact("Issued", x.Issued.In(s.LondonLoc).Format("02 January 2006 15:04"))
			fact("Remedy deadline", formatDateTime(x.Remedy, s.LondonLoc))
			fact("Appeal deadline", formatDateTime(x.Appeal, s.LondonLoc))
			fact("Served/resolved", formatDateTime(x.Served, s.LondonLoc))
			if x.Points != nil {
				fact("Points deduction", strconv.Itoa(int(*x.Points)))
			}
			fmt.Fprint(w, `</dl>`)
			if x.Ref != "" {
				if x.URL != "" {
					fmt.Fprintf(w, `<p><strong>Applicable rule:</strong> <a href="%s" target="_blank" rel="noopener">Rule %s</a></p>`, escapeHTML(x.URL), escapeHTML(x.Ref))
				} else {
					fmt.Fprintf(w, `<p><strong>Applicable rule:</strong> Rule %s</p>`, escapeHTML(x.Ref))
				}
			} else {
				fmt.Fprint(w, `<div class="alert alert-warning small">The applicable historical rule has not yet been verified by an administrator.</div>`)
			}
			fmt.Fprint(w, `<p class="small text-muted mb-0">This page explains the league record; it does not create or alter a decision. If you believe it is incorrect or wish to appeal, contact the GMCL disciplinary team and quote the week and card shown above.</p></div></article>`)
		}
		fmt.Fprint(w, `</div>`)
		pageFooter(w)
	}
}

func dateValue(v *time.Time) string {
	if v == nil {
		return ""
	}
	return v.Format("2006-01-02")
}
func dateTimeValue(v *time.Time) string {
	if v == nil {
		return ""
	}
	return v.Format("2006-01-02T15:04")
}
func parseLocalDateTime(v string, loc *time.Location) any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02T15:04", v, loc)
	if err != nil {
		return nil
	}
	return t
}
func formatDate(v *time.Time, loc *time.Location) string {
	if v == nil {
		return "Not recorded"
	}
	return v.In(loc).Format("02 January 2006")
}
func formatDateTime(v *time.Time, loc *time.Location) string {
	if v == nil {
		return ""
	}
	return v.In(loc).Format("02 January 2006 15:04")
}
func humanSanctionReason(reason, notes string) string {
	label := strings.ReplaceAll(reason, "_", " ")
	if notes != "" {
		return strings.Title(label) + ": " + notes
	}
	return strings.Title(label)
}
