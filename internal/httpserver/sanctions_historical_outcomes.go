package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type v8DecisionHistoryField struct {
	Label string
	Value string
}

var v8DecisionHistoryFieldOrder = []string{
	"Offending Club Response Text",
	"POINTS deduction",
	"Cards",
}

func v8DecisionHistoryFields(history map[string]string) []v8DecisionHistoryField {
	fields := make([]v8DecisionHistoryField, 0, len(v8DecisionHistoryFieldOrder))
	for _, key := range v8DecisionHistoryFieldOrder {
		if value := strings.TrimSpace(history[key]); value != "" {
			fields = append(fields, v8DecisionHistoryField{Label: key, Value: value})
		}
	}
	return fields
}

// immutable, non-operative history. These snapshots never imply that free-text
// writeAdminHistoricalOutcomeSnapshots renders signed-off tracker outcomes as
// points/cards were applied to a ledger.
func (s *Server) writeAdminHistoricalOutcomeSnapshots(w http.ResponseWriter, r *http.Request, caseID int64) {
	rows, err := s.DB.Query(r.Context(), `SELECT source_row_number,manual_history::text,effects_review_status,
		COALESCE(effect_interpretation,''),created_at
		FROM sanction_historical_outcome_snapshots WHERE case_id=$1 ORDER BY id`, caseID)
	if err != nil {
		return
	}
	defer rows.Close()
	type snapshot struct {
		rowNumber      int
		fields         []v8DecisionHistoryField
		reviewStatus   string
		interpretation string
		created        time.Time
	}
	var snapshots []snapshot
	for rows.Next() {
		var item snapshot
		var raw []byte
		if rows.Scan(&item.rowNumber, &raw, &item.reviewStatus, &item.interpretation, &item.created) != nil {
			continue
		}
		var history map[string]string
		_ = json.Unmarshal(raw, &history)
		item.fields = v8DecisionHistoryFields(history)
		if len(item.fields) == 0 && strings.TrimSpace(item.interpretation) == "" {
			continue
		}
		snapshots = append(snapshots, item)
	}
	if len(snapshots) == 0 {
		return
	}
	fmt.Fprint(w, `<section class="card mb-4 border-info"><div class="card-header">Imported historical tracker outcomes</div><div class="card-body"><div class="alert alert-info small">These are exact, signed-off historical records. Free-text points or cards shown here are non-operative until separately proposed and independently approved through the current decision workflow.</div>`)
	for _, item := range snapshots {
		fmt.Fprintf(w, `<details class="border rounded p-3 mb-2"><summary><strong>Tracker row %d</strong> · %s · imported %s</summary><dl class="row small mt-3 mb-0">`, item.rowNumber, escapeHTML(strings.ReplaceAll(item.reviewStatus, "_", " ")), escapeHTML(item.created.In(s.LondonLoc).Format("02 Jan 2006 15:04")))
		for _, field := range item.fields {
			fmt.Fprintf(w, `<dt class="col-sm-4">%s</dt><dd class="col-sm-8" style="white-space:pre-wrap">%s</dd>`, escapeHTML(field.Label), escapeHTML(field.Value))
		}
		if strings.TrimSpace(item.interpretation) != "" {
			fmt.Fprintf(w, `<dt class="col-sm-4">Reviewed interpretation</dt><dd class="col-sm-8" style="white-space:pre-wrap">%s</dd>`, escapeHTML(item.interpretation))
		}
		fmt.Fprint(w, `</dl></details>`)
	}
	fmt.Fprint(w, `</div></section>`)
}
