package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type snsEnvelope struct {
	Type         string `json:"Type"`
	MessageID    string `json:"MessageId"`
	Message      string `json:"Message"`
	SubscribeURL string `json:"SubscribeURL"`
}

type sesNotification struct {
	NotificationType string `json:"notificationType"`
	Mail             struct {
		Timestamp     string   `json:"timestamp"`
		Source        string   `json:"source"`
		MessageID     string   `json:"messageId"`
		Destination   []string `json:"destination"`
		CommonHeaders struct {
			Subject string `json:"subject"`
		} `json:"commonHeaders"`
	} `json:"mail"`
	Bounce struct {
		BounceType        string `json:"bounceType"`
		BounceSubType     string `json:"bounceSubType"`
		BouncedRecipients []struct {
			EmailAddress   string `json:"emailAddress"`
			DiagnosticCode string `json:"diagnosticCode"`
		} `json:"bouncedRecipients"`
	} `json:"bounce"`
	Complaint struct {
		ComplaintFeedbackType string `json:"complaintFeedbackType"`
		ComplainedRecipients  []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"complainedRecipients"`
	} `json:"complaint"`
	Delivery struct {
		Recipients []string `json:"recipients"`
	} `json:"delivery"`
}

func (s *Server) handleSESEventWebhook() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expectedToken := strings.TrimSpace(os.Getenv("SES_SNS_WEBHOOK_TOKEN"))
		if expectedToken != "" && r.URL.Query().Get("token") != expectedToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var env snsEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if env.Type == "SubscriptionConfirmation" {
			if os.Getenv("SES_SNS_AUTO_CONFIRM") == "1" && env.SubscribeURL != "" {
				go func(url string) {
					client := http.Client{Timeout: 10 * time.Second}
					_, _ = client.Get(url)
				}(env.SubscribeURL)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("subscription received"))
			return
		}

		var n sesNotification
		if err := json.Unmarshal([]byte(env.Message), &n); err != nil {
			http.Error(w, "invalid ses message", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := s.storeSESEvent(ctx, env, n); err != nil {
			http.Error(w, "store failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

func (s *Server) storeSESEvent(ctx context.Context, env snsEnvelope, n sesNotification) error {
	raw, _ := json.Marshal(n)
	occurredAt := time.Now()
	if n.Mail.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, n.Mail.Timestamp); err == nil {
			occurredAt = parsed
		}
	}

	type row struct {
		recipient             string
		bounceType            string
		bounceSubType         string
		complaintFeedbackType string
		diagnostic            string
	}
	var rows []row
	eventType := strings.ToLower(strings.TrimSpace(n.NotificationType))
	switch eventType {
	case "bounce":
		for _, r := range n.Bounce.BouncedRecipients {
			rows = append(rows, row{
				recipient:     r.EmailAddress,
				bounceType:    n.Bounce.BounceType,
				bounceSubType: n.Bounce.BounceSubType,
				diagnostic:    r.DiagnosticCode,
			})
		}
	case "complaint":
		for _, r := range n.Complaint.ComplainedRecipients {
			rows = append(rows, row{recipient: r.EmailAddress, complaintFeedbackType: n.Complaint.ComplaintFeedbackType})
		}
	case "delivery":
		for _, recipient := range n.Delivery.Recipients {
			rows = append(rows, row{recipient: recipient})
		}
	default:
		for _, recipient := range n.Mail.Destination {
			rows = append(rows, row{recipient: recipient})
		}
	}
	if len(rows) == 0 {
		rows = append(rows, row{})
	}

	for _, r := range rows {
		_, err := s.DB.Exec(ctx, `
			INSERT INTO email_events (
				provider, event_type, notification_type, message_id, ses_message_id,
				recipient, source_email, subject, bounce_type, bounce_sub_type,
				complaint_feedback_type, diagnostic_code, occurred_at, raw_json
			) VALUES (
				'amazon_ses', $1, $2, $3, $4,
				NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''),
				NULLIF($9,''), NULLIF($10,''), NULLIF($11,''), $12, $13
			)
		`, eventType, n.NotificationType, env.MessageID, n.Mail.MessageID,
			strings.TrimSpace(r.recipient), n.Mail.Source, n.Mail.CommonHeaders.Subject,
			r.bounceType, r.bounceSubType, r.complaintFeedbackType, r.diagnostic, occurredAt, raw)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleAdminEmailHealth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		days := 30
		if d := r.URL.Query().Get("days"); d != "" {
			if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 365 {
				days = n
			}
		}

		type counts struct {
			Bounces    int64
			Complaints int64
			Deliveries int64
			Other      int64
		}
		var c counts
		_ = s.DB.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE event_type='bounce'),
				COUNT(*) FILTER (WHERE event_type='complaint'),
				COUNT(*) FILTER (WHERE event_type='delivery'),
				COUNT(*) FILTER (WHERE event_type NOT IN ('bounce','complaint','delivery'))
			FROM email_events
			WHERE created_at >= now() - ($1::text || ' days')::interval
		`, days).Scan(&c.Bounces, &c.Complaints, &c.Deliveries, &c.Other)

		type eventRow struct {
			CreatedAt time.Time
			EventType string
			Recipient string
			Subject   string
			Detail    string
			Club      string
			Team      string
		}
		var events []eventRow
		rows, err := s.DB.Query(ctx, `
			SELECT ee.created_at, ee.event_type, COALESCE(ee.recipient,''),
			       COALESCE(ee.subject,''), COALESCE(NULLIF(ee.diagnostic_code,''), NULLIF(ee.bounce_type,''), NULLIF(ee.complaint_feedback_type,''), ''),
			       COALESCE(cl.name,''), COALESCE(t.name,'')
			FROM email_events ee
			LEFT JOIN captains c ON LOWER(c.email)=LOWER(ee.recipient)
			LEFT JOIN teams t ON t.id=c.team_id
			LEFT JOIN clubs cl ON cl.id=t.club_id
			WHERE ee.created_at >= now() - ($1::text || ' days')::interval
			ORDER BY ee.created_at DESC
			LIMIT 100
		`, days)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var row eventRow
				if rows.Scan(&row.CreatedAt, &row.EventType, &row.Recipient, &row.Subject, &row.Detail, &row.Club, &row.Team) == nil {
					events = append(events, row)
				}
			}
		}

		csrfToken := ""
		if ck, err := r.Cookie("csrf_token"); err == nil {
			csrfToken = ck.Value
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Email Health")
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

		fmt.Fprintf(w, `<div class="container-fluid px-4">
<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Email Health</h4>
    <p class="text-muted mb-0 small">Amazon SES bounce, complaint and delivery events received through SNS.</p>
  </div>
  <form method="GET" action="/admin/email-health" class="d-flex gap-2 align-items-center">
    <select name="days" class="form-select form-select-sm" onchange="this.form.submit()">
      <option value="7"%s>Last 7 days</option>
      <option value="30"%s>Last 30 days</option>
      <option value="90"%s>Last 90 days</option>
    </select>
  </form>
</div>`, selected(days, 7), selected(days, 30), selected(days, 90))

		fmt.Fprintf(w, `<div class="row g-3 mb-4">
<div class="col-auto"><div class="card card-kpi kpi-red p-3 text-center" style="min-width:120px"><div class="kpi-number text-danger">%d</div><div class="kpi-label">Bounces</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-gold p-3 text-center" style="min-width:120px"><div class="kpi-number">%d</div><div class="kpi-label">Complaints</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-green p-3 text-center" style="min-width:120px"><div class="kpi-number text-success">%d</div><div class="kpi-label">Deliveries</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-blue p-3 text-center" style="min-width:120px"><div class="kpi-number">%d</div><div class="kpi-label">Other</div></div></div>
</div>`, c.Bounces, c.Complaints, c.Deliveries, c.Other)

		fmt.Fprint(w, `<div class="card shadow-sm"><div class="table-responsive"><table class="table table-hover table-gmcl mb-0">
<thead><tr><th>Time</th><th>Event</th><th>Recipient</th><th>Club / Team</th><th>Subject</th><th>Detail</th></tr></thead><tbody>`)
		for _, e := range events {
			badge := "text-bg-secondary"
			if e.EventType == "bounce" {
				badge = "text-bg-danger"
			} else if e.EventType == "complaint" {
				badge = "text-bg-warning"
			} else if e.EventType == "delivery" {
				badge = "text-bg-success"
			}
			clubTeam := `<span class="text-muted">-</span>`
			if e.Club != "" || e.Team != "" {
				clubTeam = escapeHTML(strings.TrimSpace(e.Club + " " + e.Team))
			}
			fmt.Fprintf(w, `<tr><td class="small text-muted">%s</td><td><span class="badge %s">%s</span></td><td>%s</td><td>%s</td><td class="small">%s</td><td class="small text-muted">%s</td></tr>`,
				e.CreatedAt.Format("02 Jan 15:04"), badge, escapeHTML(e.EventType), escapeHTML(e.Recipient), clubTeam, escapeHTML(e.Subject), escapeHTML(e.Detail))
		}
		if len(events) == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No SES events received for this period.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></div></div>`)
		pageFooter(w)
	}
}

func selected(got, want int) string {
	if got == want {
		return " selected"
	}
	return ""
}
