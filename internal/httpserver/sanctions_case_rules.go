package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type caseAllegedRule struct {
	ID          int64
	Revision    int
	ReleaseID   int64
	Reference   string
	Heading     string
	Text        string
	URL         string
	SourceTitle string
	Reason      string
	SelectedBy  string
	SelectedAt  time.Time
}

func normalizeRuleReference(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 5 && strings.EqualFold(value[:5], "rule ") {
		value = strings.TrimSpace(value[5:])
	}
	return value
}

func loadCaseAllegedRule(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, caseID int64) (caseAllegedRule, error) {
	var rule caseAllegedRule
	err := query.QueryRow(ctx, `SELECT allegation.id,allegation.revision,allegation.rule_release_id,
		allegation.rule_reference,allegation.heading_path,allegation.rule_text,allegation.canonical_url,
		allegation.source_title,allegation.selection_reason,COALESCE(admin.username,''),allegation.selected_at
		FROM sanction_case_rule_allegation_revisions allegation
		LEFT JOIN admin_users admin ON admin.id=allegation.selected_by_admin_id
		WHERE allegation.case_id=$1 ORDER BY allegation.revision DESC,allegation.id DESC LIMIT 1`, caseID).
		Scan(&rule.ID, &rule.Revision, &rule.ReleaseID, &rule.Reference, &rule.Heading, &rule.Text,
			&rule.URL, &rule.SourceTitle, &rule.Reason, &rule.SelectedBy, &rule.SelectedAt)
	return rule, err
}

func allegedRuleCorrespondenceParagraph(rule caseAllegedRule) string {
	if strings.TrimSpace(rule.Reference) == "" {
		return ""
	}
	heading := strings.TrimSpace(rule.Heading)
	if heading == "" {
		return "Alleged rule under investigation: Rule " + strings.TrimSpace(rule.Reference)
	}
	paragraph := "Alleged rule under investigation: Rule " + strings.TrimSpace(rule.Reference) + " - " + heading
	if sourceURL := strings.TrimSpace(rule.URL); sourceURL != "" {
		paragraph += "\nPublished source: " + sourceURL
	}
	return paragraph
}

func recordStarredCaseAllegedRule(ctx context.Context, tx pgx.Tx, caseID int64, adminID *int32, actorLabel, requestID string) error {
	var allegationID, releaseID int64
	var reference, canonicalURL string
	err := tx.QueryRow(ctx, `WITH published_rule AS (
		SELECT release.id AS release_id,chunk.rule_reference,chunk.heading_path,chunk.content,
		       document.canonical_url,document.title
		FROM rule_releases release JOIN rule_chunks chunk ON chunk.release_id=release.id
		JOIN rule_documents document ON document.id=chunk.document_id
		WHERE release.status='active' AND LOWER(BTRIM(chunk.rule_reference))='3.5'
		ORDER BY chunk.ordinal,chunk.id LIMIT 1
	), inserted AS (
		INSERT INTO sanction_case_rule_allegation_revisions(case_id,revision,rule_release_id,rule_reference,
			heading_path,rule_text,canonical_url,source_title,selection_reason,selected_by_admin_id)
		SELECT $1,1,release_id,rule_reference,heading_path,content,canonical_url,title,
			'Revalidated starred-player finding identifies Rule 3.5',$2 FROM published_rule
		RETURNING id,rule_release_id,rule_reference,canonical_url
	) SELECT id,rule_release_id,rule_reference,canonical_url FROM inserted`, caseID, adminID).
		Scan(&allegationID, &releaseID, &reference, &canonicalURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,request_id)
		VALUES($1,'alleged_rule_recorded','admin',$2,$3,'Rule 3.5 recorded from revalidated starred-player finding',
		jsonb_build_object('allegation_revision_id',$4::bigint,'rule_release_id',$5::bigint,'rule_reference',$6::text,'canonical_url',$7::text),$8)`,
		caseID, adminID, actorLabel, allegationID, releaseID, reference, canonicalURL, requestID)
	return err
}

func (s *Server) handleAdminCaseAllegedRuleSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := parsePositiveCaseID(chi.URLParam(r, "id"))
		if err != nil || r.ParseForm() != nil {
			http.Error(w, "invalid rule selection", http.StatusBadRequest)
			return
		}
		reference := normalizeRuleReference(r.FormValue("rule_reference"))
		reason := strings.TrimSpace(r.FormValue("selection_reason"))
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "administrator identity is required", http.StatusUnauthorized)
			return
		}
		if reference == "" || reason == "" || len(reference) > 100 || len(reason) > 2000 {
			http.Error(w, "published rule reference and review reason are required", http.StatusBadRequest)
			return
		}
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "could not record alleged rule", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var status, sourceType string
		if err = tx.QueryRow(r.Context(), `SELECT status,source_type FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&status, &sourceType); err != nil {
			http.NotFound(w, r)
			return
		}
		if sourceType != "ineligible_player" {
			http.Error(w, "alleged-rule selection is currently limited to ineligible-player investigations", http.StatusConflict)
			return
		}
		if !map[string]bool{"submitted": true, "triage": true, "investigating": true}[status] {
			http.Error(w, "the alleged rule is locked once a response is requested or a decision is proposed", http.StatusConflict)
			return
		}
		var releaseID int64
		var resolvedReference, heading, ruleText, canonicalURL, sourceTitle string
		err = tx.QueryRow(r.Context(), `SELECT release.id,chunk.rule_reference,chunk.heading_path,chunk.content,document.canonical_url,document.title
			FROM rule_releases release JOIN rule_chunks chunk ON chunk.release_id=release.id
			JOIN rule_documents document ON document.id=chunk.document_id
			WHERE release.status='active' AND LOWER(BTRIM(chunk.rule_reference))=LOWER(BTRIM($1))
			ORDER BY chunk.ordinal,chunk.id LIMIT 1`, reference).
			Scan(&releaseID, &resolvedReference, &heading, &ruleText, &canonicalURL, &sourceTitle)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "that reference was not found in the currently published GMCL rules; choose a listed rule", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "published rules could not be checked", http.StatusInternalServerError)
			return
		}
		var allegationID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_rule_allegation_revisions(
			case_id,revision,supersedes_id,rule_release_id,rule_reference,heading_path,rule_text,
			canonical_url,source_title,selection_reason,selected_by_admin_id)
			VALUES($1,(SELECT COALESCE(MAX(revision),0)+1 FROM sanction_case_rule_allegation_revisions WHERE case_id=$1),
			(SELECT id FROM sanction_case_rule_allegation_revisions WHERE case_id=$1 ORDER BY revision DESC,id DESC LIMIT 1),
			$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, caseID, releaseID, resolvedReference, heading,
			ruleText, canonicalURL, sourceTitle, reason, *actor.ID).Scan(&allegationID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,request_id)
				VALUES($1,'alleged_rule_recorded','admin',$2,$3,$4,jsonb_build_object('allegation_revision_id',$5::bigint,'rule_release_id',$6::bigint,'rule_reference',$7::text,'canonical_url',$8::text),$9)`,
				caseID, *actor.ID, actor.Label, reason, allegationID, releaseID, resolvedReference, canonicalURL, actor.RequestID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not record alleged rule", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s", caseID, urlQueryEscape("Rule "+resolvedReference+" recorded for the investigation. Save fresh correspondence drafts before sending.")), http.StatusSeeOther)
	}
}

func (s *Server) writeAdminCaseAllegedRule(w http.ResponseWriter, ctx context.Context, caseID int64, status, csrf string) caseAllegedRule {
	rule, err := loadCaseAllegedRule(ctx, s.DB, caseID)
	if err == nil {
		excerpt := strings.TrimSpace(rule.Text)
		if len(excerpt) > 700 {
			excerpt = excerpt[:700] + "..."
		}
		fmt.Fprintf(w, `<section class="card mb-4 border-primary"><div class="card-header">Alleged rule under investigation</div><div class="card-body"><div class="d-flex justify-content-between gap-2"><div><strong>Rule %s</strong><div>%s</div></div><span class="badge text-bg-light border">revision %d</span></div><p class="small mt-3 mb-2">%s</p><a href="%s" target="_blank" rel="noopener noreferrer">Open published source: %s</a><div class="small text-muted mt-2">Selected %s by %s. Reason: %s</div></div></section>`,
			escapeHTML(rule.Reference), escapeHTML(rule.Heading), rule.Revision, escapeHTML(excerpt), escapeHTML(rule.URL), escapeHTML(rule.SourceTitle),
			escapeHTML(rule.SelectedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")), escapeHTML(rule.SelectedBy), escapeHTML(rule.Reason))
	} else {
		fmt.Fprint(w, `<div class="alert alert-warning"><strong>No alleged rule recorded.</strong> Select the published rule being investigated before preparing correspondence to the offending club.</div>`)
	}
	if !map[string]bool{"submitted": true, "triage": true, "investigating": true}[status] {
		return rule
	}
	rows, queryErr := s.DB.Query(ctx, `SELECT DISTINCT ON (LOWER(BTRIM(chunk.rule_reference))) chunk.rule_reference,chunk.heading_path
		FROM rule_releases release JOIN rule_chunks chunk ON chunk.release_id=release.id
		WHERE release.status='active' AND NULLIF(BTRIM(chunk.rule_reference),'') IS NOT NULL
		ORDER BY LOWER(BTRIM(chunk.rule_reference)),chunk.ordinal,chunk.id`)
	options := ""
	if queryErr == nil {
		defer rows.Close()
		var builder strings.Builder
		for rows.Next() {
			var reference, heading string
			if rows.Scan(&reference, &heading) == nil {
				fmt.Fprintf(&builder, `<option value="%s">%s</option>`, escapeHTML(reference), escapeHTML(heading))
			}
		}
		options = builder.String()
	}
	fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/alleged-rule" class="card mb-4"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">%s alleged rule</div><div class="card-body"><label class="form-label">Published GMCL rule reference</label><input class="form-control" name="rule_reference" list="published-rule-references" value="%s" placeholder="e.g. 3.5" maxlength="100" required><datalist id="published-rule-references">%s</datalist><div class="form-text">Choose a reference from the active published rules. Hawk AI can help identify candidates, but an investigator must confirm the selection.</div><label class="form-label mt-3">Why this rule is relevant to the allegation</label><textarea class="form-control" name="selection_reason" rows="2" maxlength="2000" required></textarea></div><div class="card-footer d-flex justify-content-between align-items-center"><a href="/admin/rules-assistant" target="_blank" rel="noopener">Check with Hawk AI</a><button class="btn btn-outline-primary">Record immutable rule revision</button></div></form>`, caseID, escapeHTML(csrf), map[bool]string{true: "Revise", false: "Record"}[rule.ID > 0], escapeHTML(rule.Reference), options)
	return rule
}
