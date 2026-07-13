package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	TopicARN     string `json:"TopicArn"`
}

type sesNotification struct {
	EventType        string `json:"eventType"`
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
		Timestamp         string `json:"timestamp"`
		BounceType        string `json:"bounceType"`
		BounceSubType     string `json:"bounceSubType"`
		BouncedRecipients []struct {
			EmailAddress   string `json:"emailAddress"`
			DiagnosticCode string `json:"diagnosticCode"`
		} `json:"bouncedRecipients"`
	} `json:"bounce"`
	Complaint struct {
		Timestamp             string `json:"timestamp"`
		ComplaintFeedbackType string `json:"complaintFeedbackType"`
		ComplainedRecipients  []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"complainedRecipients"`
	} `json:"complaint"`
	Delivery struct {
		Timestamp  string   `json:"timestamp"`
		Recipients []string `json:"recipients"`
	} `json:"delivery"`
	DeliveryDelay struct {
		Timestamp         string `json:"timestamp"`
		DelayType         string `json:"delayType"`
		DelayedRecipients []struct {
			EmailAddress   string `json:"emailAddress"`
			DiagnosticCode string `json:"diagnosticCode"`
		} `json:"delayedRecipients"`
	} `json:"deliveryDelay"`
	Reject struct {
		Reason string `json:"reason"`
	} `json:"reject"`
	Failure struct {
		ErrorMessage string `json:"errorMessage"`
	} `json:"failure"`
	Open struct {
		IPAddress string `json:"ipAddress"`
		UserAgent string `json:"userAgent"`
	} `json:"open"`
	Click struct {
		IPAddress string `json:"ipAddress"`
		UserAgent string `json:"userAgent"`
		Link      string `json:"link"`
	} `json:"click"`
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

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		env, n, deliveryMode, err := decodeSESWebhook(body, r.Header)
		if err != nil {
			s.recordSESWebhookReceipt(ctx, env, deliveryMode, "rejected", err.Error())
			http.Error(w, "invalid SES/SNS payload", http.StatusBadRequest)
			return
		}
		if env.Type == "SubscriptionConfirmation" {
			status := "pending_confirmation"
			detail := "Set SES_SNS_AUTO_CONFIRM=1 temporarily or confirm the subscription in Amazon SNS."
			if os.Getenv("SES_SNS_AUTO_CONFIRM") == "1" && env.SubscribeURL != "" {
				if !validSNSSubscribeURL(env.SubscribeURL) {
					status, detail = "confirmation_rejected", "SNS SubscribeURL was not an expected Amazon SNS HTTPS URL."
				} else {
					client := http.Client{Timeout: 8 * time.Second}
					resp, confirmErr := client.Get(env.SubscribeURL)
					if confirmErr != nil {
						status, detail = "confirmation_failed", confirmErr.Error()
					} else {
						_ = resp.Body.Close()
						if resp.StatusCode >= 200 && resp.StatusCode < 300 {
							status, detail = "confirmed", "SNS subscription confirmed successfully."
						} else {
							status, detail = "confirmation_failed", resp.Status
						}
					}
				}
			}
			s.recordSESWebhookReceipt(ctx, env, deliveryMode, status, detail)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(status))
			return
		}

		if err := s.storeSESEvent(ctx, env, n); err != nil {
			s.recordSESWebhookReceipt(ctx, env, deliveryMode, "store_failed", err.Error())
			http.Error(w, "store failed", http.StatusInternalServerError)
			return
		}
		s.recordSESWebhookReceipt(ctx, env, deliveryMode, "stored", sesEventType(n))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

func decodeSESWebhook(body []byte, header http.Header) (snsEnvelope, sesNotification, string, error) {
	var env snsEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return env, sesNotification{}, "unknown", fmt.Errorf("invalid JSON: %w", err)
	}
	if env.MessageID == "" {
		env.MessageID = strings.TrimSpace(header.Get("x-amz-sns-message-id"))
	}
	if env.TopicARN == "" {
		env.TopicARN = strings.TrimSpace(header.Get("x-amz-sns-topic-arn"))
	}
	if env.Type == "SubscriptionConfirmation" {
		return env, sesNotification{}, "sns_wrapped", nil
	}
	if env.Type != "" {
		if env.Type != "Notification" || strings.TrimSpace(env.Message) == "" {
			return env, sesNotification{}, "sns_wrapped", fmt.Errorf("unsupported SNS message type %q", env.Type)
		}
		var n sesNotification
		if err := json.Unmarshal([]byte(env.Message), &n); err != nil {
			return env, n, "sns_wrapped", fmt.Errorf("invalid wrapped SES event: %w", err)
		}
		if sesEventType(n) == "" {
			return env, n, "sns_wrapped", fmt.Errorf("SES event has no eventType or notificationType")
		}
		return env, n, "sns_wrapped", nil
	}

	var n sesNotification
	if err := json.Unmarshal(body, &n); err != nil {
		return env, n, "sns_raw", fmt.Errorf("invalid raw SES event: %w", err)
	}
	if sesEventType(n) == "" {
		return env, n, "sns_raw", fmt.Errorf("raw SES event has no eventType or notificationType")
	}
	return env, n, "sns_raw", nil
}

func sesEventType(n sesNotification) string {
	eventType := sesOriginalEventType(n)
	return strings.NewReplacer(" ", "_", "-", "_").Replace(strings.ToLower(eventType))
}

func sesOriginalEventType(n sesNotification) string {
	if eventType := strings.TrimSpace(n.EventType); eventType != "" {
		return eventType
	}
	return strings.TrimSpace(n.NotificationType)
}

func validSNSSubscribeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Path != "/" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "sns.amazonaws.com" && (!strings.HasPrefix(host, "sns.") || !strings.HasSuffix(host, ".amazonaws.com")) {
		return false
	}
	return u.Query().Get("Action") == "ConfirmSubscription"
}

func (s *Server) recordSESWebhookReceipt(ctx context.Context, env snsEnvelope, deliveryMode, status, detail string) {
	_, _ = s.DB.Exec(ctx, `
		INSERT INTO ses_webhook_receipts
		    (sns_message_id, message_type, delivery_mode, topic_arn, status, detail)
		VALUES (NULLIF($1,''), NULLIF($2,''), $3, NULLIF($4,''), $5, NULLIF($6,''))
		ON CONFLICT (sns_message_id) WHERE sns_message_id IS NOT NULL
		DO UPDATE SET status=EXCLUDED.status, detail=EXCLUDED.detail, received_at=now()
	`, env.MessageID, env.Type, deliveryMode, env.TopicARN, status, detail)
}

func (s *Server) storeSESEvent(ctx context.Context, env snsEnvelope, n sesNotification) error {
	raw, _ := json.Marshal(n)
	occurredAt := time.Now()
	eventType := sesEventType(n)
	eventTimestamp := n.Mail.Timestamp
	switch eventType {
	case "bounce":
		eventTimestamp = n.Bounce.Timestamp
	case "complaint":
		eventTimestamp = n.Complaint.Timestamp
	case "delivery":
		eventTimestamp = n.Delivery.Timestamp
	case "deliverydelay":
		eventTimestamp = n.DeliveryDelay.Timestamp
	}
	if eventTimestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, eventTimestamp); err == nil {
			occurredAt = parsed
		}
	}

	type row struct {
		recipient             string
		bounceType            string
		bounceSubType         string
		complaintFeedbackType string
		diagnostic            string
		linkURL               string
		clickIP               string
		clickUserAgent        string
		linkContext           *magicLinkEventContext
	}
	var rows []row
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
	case "deliverydelay":
		for _, r := range n.DeliveryDelay.DelayedRecipients {
			rows = append(rows, row{recipient: r.EmailAddress, diagnostic: strings.TrimSpace(n.DeliveryDelay.DelayType + " " + r.DiagnosticCode)})
		}
	case "reject":
		for _, recipient := range n.Mail.Destination {
			rows = append(rows, row{recipient: recipient, diagnostic: n.Reject.Reason})
		}
	case "rendering_failure":
		for _, recipient := range n.Mail.Destination {
			rows = append(rows, row{recipient: recipient, diagnostic: n.Failure.ErrorMessage})
		}
	case "open":
		for _, recipient := range n.Mail.Destination {
			rows = append(rows, row{recipient: recipient, diagnostic: strings.TrimSpace(n.Open.IPAddress + " " + n.Open.UserAgent)})
		}
	case "click":
		var linkContext *magicLinkEventContext
		if ctxData, ok := s.magicLinkContextForURL(ctx, n.Click.Link); ok {
			linkContext = &ctxData
		}
		for _, recipient := range n.Mail.Destination {
			detail := strings.TrimSpace(n.Click.IPAddress + " " + n.Click.UserAgent)
			if n.Click.Link != "" {
				detail = strings.TrimSpace(detail + " " + n.Click.Link)
			}
			rows = append(rows, row{
				recipient:      recipient,
				diagnostic:     detail,
				linkURL:        n.Click.Link,
				clickIP:        n.Click.IPAddress,
				clickUserAgent: n.Click.UserAgent,
				linkContext:    linkContext,
			})
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
		var tokenID, captainID, teamID, seasonID, weekID, matchDate any
		if r.linkContext != nil {
			tokenID = r.linkContext.TokenID
			captainID = r.linkContext.CaptainID
			teamID = r.linkContext.TeamID
			seasonID = r.linkContext.SeasonID
			weekID = r.linkContext.WeekID
			if r.linkContext.MatchDate != nil {
				matchDate = *r.linkContext.MatchDate
			}
		}
		_, err := s.DB.Exec(ctx, `
			INSERT INTO email_events (
				provider, event_type, notification_type, message_id, ses_message_id,
				recipient, source_email, subject, bounce_type, bounce_sub_type,
				complaint_feedback_type, diagnostic_code, occurred_at, raw_json,
				magic_link_token_id, captain_id, team_id, season_id, week_id,
				match_date, link_url, click_ip, click_user_agent
			) VALUES (
				'amazon_ses', $1, $2, $3, $4,
				NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''),
				NULLIF($9,''), NULLIF($10,''), NULLIF($11,''), $12, $13,
				$14, $15, $16, $17, $18,
				$19, NULLIF($20,''), NULLIF($21,'')::inet, NULLIF($22,'')
			)
		`, eventType, sesOriginalEventType(n), env.MessageID, n.Mail.MessageID,
			strings.TrimSpace(r.recipient), n.Mail.Source, n.Mail.CommonHeaders.Subject,
			r.bounceType, r.bounceSubType, r.complaintFeedbackType, r.diagnostic, occurredAt, raw,
			tokenID, captainID, teamID, seasonID, weekID, matchDate, r.linkURL, r.clickIP, r.clickUserAgent)
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
			Accepted    int64
			Failures    int64
			Bounces     int64
			SoftBounces int64
			HardBounces int64
			Complaints  int64
			Deliveries  int64
			Opens       int64
			Clicks      int64
			Other       int64
		}
		var c counts
		_ = s.DB.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM captain_reminder_log
				 WHERE sent_at >= now() - make_interval(days => $1)),
				(SELECT COUNT(*) FROM captain_reminder_failures
				 WHERE created_at >= now() - make_interval(days => $1))
		`, days).Scan(&c.Accepted, &c.Failures)
		_ = s.DB.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE event_type='bounce'),
				COUNT(*) FILTER (WHERE event_type='bounce' AND LOWER(COALESCE(bounce_type,''))='transient'),
				COUNT(*) FILTER (WHERE event_type='bounce' AND LOWER(COALESCE(bounce_type,''))='permanent'),
				COUNT(*) FILTER (WHERE event_type='complaint'),
				COUNT(*) FILTER (WHERE event_type='delivery'),
				COUNT(*) FILTER (WHERE event_type='open'),
				COUNT(*) FILTER (WHERE event_type='click'),
				COUNT(*) FILTER (WHERE event_type NOT IN ('bounce','complaint','delivery','open','click'))
			FROM email_events
			WHERE created_at >= now() - make_interval(days => $1)
		`, days).Scan(&c.Bounces, &c.SoftBounces, &c.HardBounces, &c.Complaints, &c.Deliveries, &c.Opens, &c.Clicks, &c.Other)

		type webhookReceiptRow struct {
			ReceivedAt   time.Time
			MessageType  string
			DeliveryMode string
			Status       string
			Detail       string
		}
		var webhookReceipts []webhookReceiptRow
		receiptRows, err := s.DB.Query(ctx, `
			SELECT received_at, COALESCE(message_type,''), delivery_mode, status, COALESCE(detail,'')
			FROM ses_webhook_receipts
			ORDER BY received_at DESC
			LIMIT 10
		`)
		if err == nil {
			defer receiptRows.Close()
			for receiptRows.Next() {
				var row webhookReceiptRow
				if receiptRows.Scan(&row.ReceivedAt, &row.MessageType, &row.DeliveryMode, &row.Status, &row.Detail) == nil {
					webhookReceipts = append(webhookReceipts, row)
				}
			}
		}

		type sentRow struct {
			SentAt       time.Time
			MatchDate    time.Time
			ReminderType string
			Recipient    string
			Club         string
			Team         string
			Status       string
			StatusAt     *time.Time
			Detail       string
		}
		var sentEmails []sentRow
		sentRows, err := s.DB.Query(ctx, `
			SELECT crl.sent_at, crl.match_date, crl.reminder_type, crl.captain_email,
			       COALESCE(cl.name, ''), COALESCE(t.name, ''),
			       COALESCE(ev.event_type, 'accepted'), ev.created_at,
			       COALESCE(ev.detail, '')
			FROM captain_reminder_log crl
			JOIN teams t ON t.id = crl.team_id
			JOIN clubs cl ON cl.id = t.club_id
			LEFT JOIN LATERAL (
				SELECT ee.event_type, ee.created_at,
				       COALESCE(NULLIF(TRIM(CONCAT_WS(' ', ee.bounce_type, ee.bounce_sub_type, ee.diagnostic_code)), ''),
				                NULLIF(ee.complaint_feedback_type, ''), '') AS detail
				FROM email_events ee
				WHERE LOWER(ee.recipient) = LOWER(crl.captain_email)
				  AND ee.created_at >= crl.sent_at - interval '5 minutes'
				  AND ee.created_at <= crl.sent_at + interval '14 days'
				  AND (ee.subject IS NULL OR ee.subject = '' OR
				       (ee.subject ILIKE ('%' || t.name || '%')
				        AND ee.subject ILIKE ('%' || crl.match_date::text || '%')))
				ORDER BY CASE ee.event_type
				           WHEN 'bounce' THEN 1 WHEN 'complaint' THEN 2
				           WHEN 'delivery' THEN 3 WHEN 'open' THEN 4
				           WHEN 'click' THEN 5 ELSE 6 END,
				         ee.created_at DESC
				LIMIT 1
			) ev ON TRUE
			WHERE crl.sent_at >= now() - make_interval(days => $1)
			ORDER BY crl.sent_at DESC
			LIMIT 200
		`, days)
		if err == nil {
			defer sentRows.Close()
			for sentRows.Next() {
				var row sentRow
				if sentRows.Scan(&row.SentAt, &row.MatchDate, &row.ReminderType, &row.Recipient,
					&row.Club, &row.Team, &row.Status, &row.StatusAt, &row.Detail) == nil {
					sentEmails = append(sentEmails, row)
				}
			}
		}

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
			       COALESCE(ee.subject,''),
			       COALESCE(
			           NULLIF(ee.link_url, ''),
			           NULLIF(ee.diagnostic_code,''),
			           NULLIF(ee.bounce_type,''),
			           NULLIF(ee.complaint_feedback_type,''),
			           ''
			       ),
			       COALESCE(ecl.name, cl.name, ''), COALESCE(et.name, t.name, '')
			FROM email_events ee
			LEFT JOIN teams et ON et.id = ee.team_id
			LEFT JOIN clubs ecl ON ecl.id = et.club_id
			LEFT JOIN captains c ON LOWER(c.email)=LOWER(ee.recipient)
			LEFT JOIN teams t ON t.id=c.team_id
			LEFT JOIN clubs cl ON cl.id=t.club_id
			WHERE ee.created_at >= now() - make_interval(days => $1)
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

		type reminderFailureRow struct {
			CreatedAt    time.Time
			MatchDate    time.Time
			ReminderType string
			Recipient    string
			Stage        string
			ErrorMessage string
			Club         string
			Team         string
		}
		var reminderFailures []reminderFailureRow
		failureRows, err := s.DB.Query(ctx, `
			SELECT rf.created_at, rf.match_date, rf.reminder_type, rf.captain_email,
			       rf.stage, rf.error_message, COALESCE(cl.name,''), COALESCE(t.name,'')
			FROM captain_reminder_failures rf
			JOIN teams t ON t.id = rf.team_id
			JOIN clubs cl ON cl.id = t.club_id
			WHERE rf.created_at >= now() - make_interval(days => $1)
			ORDER BY rf.created_at DESC
			LIMIT 50
		`, days)
		if err == nil {
			defer failureRows.Close()
			for failureRows.Next() {
				var row reminderFailureRow
				if failureRows.Scan(&row.CreatedAt, &row.MatchDate, &row.ReminderType, &row.Recipient, &row.Stage, &row.ErrorMessage, &row.Club, &row.Team) == nil {
					reminderFailures = append(reminderFailures, row)
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

		if strings.TrimSpace(os.Getenv("SMTP_HOST")) == "" {
			fmt.Fprint(w, `<div class="alert alert-danger"><strong>Email sending is not configured.</strong> SMTP_HOST is empty, so the application only logs emails and does not hand them to SES.</div>`)
		} else if strings.TrimSpace(os.Getenv("SES_CONFIGURATION_SET")) == "" || strings.TrimSpace(os.Getenv("SES_SNS_WEBHOOK_TOKEN")) == "" {
			fmt.Fprint(w, `<div class="alert alert-warning"><strong>SES tracking is incomplete.</strong> Sends may work, but delivery and bounce results will not appear reliably until SES_CONFIGURATION_SET and SES_SNS_WEBHOOK_TOKEN are configured.</div>`)
		}
		if len(webhookReceipts) == 0 {
			fmt.Fprint(w, `<div class="alert alert-danger"><strong>No SES/SNS webhook has reached this application.</strong> Confirm the HTTPS subscription in Amazon SNS and ensure the SES configuration set event destination publishes to that topic.</div>`)
		} else {
			latest := webhookReceipts[0]
			alertClass := "alert-success"
			if latest.Status == "pending_confirmation" {
				alertClass = "alert-warning"
			} else if latest.Status != "stored" && latest.Status != "confirmed" {
				alertClass = "alert-danger"
			}
			fmt.Fprintf(w, `<div class="alert %s"><strong>Latest SES/SNS webhook:</strong> %s at %s — %s</div>`,
				alertClass, escapeHTML(latest.Status), latest.ReceivedAt.Format("02 Jan 2006 15:04"), escapeHTML(latest.Detail))
		}

		fmt.Fprintf(w, `<div class="row g-3 mb-4">
<div class="col-auto"><div class="card card-kpi kpi-blue p-3 text-center" style="min-width:120px"><div class="kpi-number">%d</div><div class="kpi-label">Send accepted</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-red p-3 text-center" style="min-width:120px"><div class="kpi-number text-danger">%d</div><div class="kpi-label">Send failures</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-red p-3 text-center" style="min-width:120px"><div class="kpi-number text-danger">%d</div><div class="kpi-label">Bounces</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-gold p-3 text-center" style="min-width:120px"><div class="kpi-number text-warning">%d</div><div class="kpi-label">Soft bounces</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-red p-3 text-center" style="min-width:120px"><div class="kpi-number text-danger">%d</div><div class="kpi-label">Hard bounces</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-gold p-3 text-center" style="min-width:120px"><div class="kpi-number">%d</div><div class="kpi-label">Complaints</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-green p-3 text-center" style="min-width:120px"><div class="kpi-number text-success">%d</div><div class="kpi-label">Deliveries</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-blue p-3 text-center" style="min-width:120px"><div class="kpi-number">%d</div><div class="kpi-label">Opens</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-purple p-3 text-center" style="min-width:120px"><div class="kpi-number">%d</div><div class="kpi-label">Clicks</div></div></div>
<div class="col-auto"><div class="card card-kpi kpi-blue p-3 text-center" style="min-width:120px"><div class="kpi-number">%d</div><div class="kpi-label">Other</div></div></div>
</div>`, c.Accepted, c.Failures, c.Bounces, c.SoftBounces, c.HardBounces, c.Complaints, c.Deliveries, c.Opens, c.Clicks, c.Other)

		fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">SES/SNS Webhook Diagnostics</div><div class="table-responsive"><table class="table table-sm table-gmcl mb-0">
<thead><tr><th>Received</th><th>SNS type</th><th>Mode</th><th>Status</th><th>Detail</th></tr></thead><tbody>`)
		for _, receipt := range webhookReceipts {
			fmt.Fprintf(w, `<tr><td class="small text-muted">%s</td><td>%s</td><td>%s</td><td>%s</td><td class="small text-muted">%s</td></tr>`,
				receipt.ReceivedAt.Format("02 Jan 15:04"), escapeHTML(receipt.MessageType), escapeHTML(receipt.DeliveryMode), escapeHTML(receipt.Status), escapeHTML(receipt.Detail))
		}
		if len(webhookReceipts) == 0 {
			fmt.Fprint(w, `<tr><td colspan="5" class="text-center text-muted py-3">No webhook requests recorded.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></div>`)

		fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header"><div class="fw-semibold">Reminder Email Ledger</div><div class="small text-muted">Every reminder accepted by the configured SMTP server, with the strongest SES result received for that message.</div></div><div class="table-responsive"><table class="table table-hover table-gmcl mb-0">
<thead><tr><th>Sent</th><th>Match date</th><th>Type</th><th>Recipient</th><th>Club / Team</th><th>Status</th><th>Result time</th><th>Detail</th></tr></thead><tbody>`)
		for _, e := range sentEmails {
			badge := "text-bg-secondary"
			label := e.Status
			switch e.Status {
			case "bounce":
				badge, label = "text-bg-danger", "Bounced"
			case "complaint":
				badge, label = "text-bg-warning", "Complaint"
			case "delivery":
				badge, label = "text-bg-success", "Delivered"
			case "open":
				badge, label = "text-bg-info", "Opened"
			case "click":
				badge, label = "text-bg-info", "Clicked"
			case "accepted":
				label = "Accepted; awaiting SES"
			}
			statusAt := `<span class="text-muted">-</span>`
			if e.StatusAt != nil {
				statusAt = escapeHTML(e.StatusAt.Format("02 Jan 15:04"))
			}
			fmt.Fprintf(w, `<tr><td class="small text-muted">%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td><span class="badge %s">%s</span></td><td class="small text-muted">%s</td><td class="small text-muted">%s</td></tr>`,
				e.SentAt.Format("02 Jan 15:04"), e.MatchDate.Format("02 Jan 2006"),
				escapeHTML(e.ReminderType), escapeHTML(e.Recipient),
				escapeHTML(strings.TrimSpace(e.Club+" "+e.Team)), badge, escapeHTML(label),
				statusAt, escapeHTML(redactMagicTokenInText(e.Detail)))
		}
		if len(sentEmails) == 0 {
			fmt.Fprint(w, `<tr><td colspan="8" class="text-center text-muted py-3">No reminder emails were accepted for this period.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></div>`)

		fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Reminder Send Failures</div><div class="table-responsive"><table class="table table-hover table-gmcl mb-0">
<thead><tr><th>Time</th><th>Match date</th><th>Type</th><th>Recipient</th><th>Club / Team</th><th>Stage</th><th>Error</th></tr></thead><tbody>`)
		for _, f := range reminderFailures {
			clubTeam := strings.TrimSpace(f.Club + " " + f.Team)
			if clubTeam == "" {
				clubTeam = "-"
			}
			fmt.Fprintf(w, `<tr><td class="small text-muted">%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td><span class="badge text-bg-danger">%s</span></td><td class="small text-muted">%s</td></tr>`,
				f.CreatedAt.Format("02 Jan 15:04"),
				f.MatchDate.Format("02 Jan 2006"),
				escapeHTML(f.ReminderType),
				escapeHTML(f.Recipient),
				escapeHTML(clubTeam),
				escapeHTML(f.Stage),
				escapeHTML(f.ErrorMessage))
		}
		if len(reminderFailures) == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-center text-muted py-3">No reminder send failures recorded for this period.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></div>`)

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
				e.CreatedAt.Format("02 Jan 15:04"), badge, escapeHTML(e.EventType), escapeHTML(e.Recipient), clubTeam, escapeHTML(e.Subject), escapeHTML(redactMagicTokenInText(e.Detail)))
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
