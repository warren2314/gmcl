package httpserver

import (
	"context"
	"fmt"
	"strings"
)

type adminCaseSourceReport struct {
	IntakeID                                                  int64
	Player, Reason, AdditionalInfo, AdditionalEvidence, Score string
	OffendingClub, Team, FixtureDate                          string
}

func adminCaseSourceReportFromRaw(intakeID int64, raw []byte) adminCaseSourceReport {
	return adminCaseSourceReport{
		IntakeID:           intakeID,
		Player:             sourceStringField(raw, "Name of defaulting player as shown on scorecard", "player"),
		Reason:             sourceStringField(raw, "Reason you believe the player is ineligible", "reason", "allegation"),
		AdditionalInfo:     sourceStringField(raw, "Additional Info", "additional information"),
		AdditionalEvidence: sourceStringField(raw, "Additional Evidence", "additional evidence"),
		Score:              sourceStringField(raw, "Score", "scorecard reference"),
		OffendingClub:      sourceStringField(raw, "Offending Club's Name", "offending club"),
		Team:               sourceStringField(raw, "Team in question", "team"),
		FixtureDate:        sourceStringField(raw, "Fixture Date", "fixture date"),
	}
}

func adminCaseSourceReportHTML(report adminCaseSourceReport) string {
	if report.IntakeID <= 0 {
		return ""
	}
	value := func(input string) string {
		if strings.TrimSpace(input) == "" {
			return "<span class=\"text-muted\">Not provided</span>"
		}
		return "<span style=\"white-space:pre-wrap\">" + escapeHTML(input) + "</span>"
	}
	rows := [][2]string{
		{"Offending club", report.OffendingClub}, {"Team in question", report.Team},
		{"Fixture date", report.FixtureDate}, {"Defaulting player(s)", report.Player},
		{"Reason believed ineligible", report.Reason}, {"Additional information", report.AdditionalInfo},
		{"Additional evidence or links", report.AdditionalEvidence}, {"Score / scorecard reference", report.Score},
	}
	var out strings.Builder
	out.WriteString("<section class=\"card mb-4 border-primary\"><div class=\"card-header\"><strong>Original report details</strong> <span class=\"badge text-bg-light border\">Private case information</span></div><div class=\"card-body\"><dl class=\"row mb-0\">")
	for _, row := range rows {
		fmt.Fprintf(&out, "<dt class=\"col-sm-4\">%s</dt><dd class=\"col-sm-8\">%s</dd>", escapeHTML(row[0]), value(row[1]))
	}
	fmt.Fprintf(&out, "</dl><a class=\"btn btn-sm btn-outline-primary mt-3\" href=\"/admin/ineligible/%d\">Open original intake and uploaded files</a></div></section>", report.IntakeID)
	return out.String()
}

func (s *Server) loadAdminCaseSourceReportHTML(ctx context.Context, caseID int64) string {
	var intakeID int64
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT intake.id,revision.raw_data
		FROM sanction_intake_case_links link
		JOIN sanction_intakes intake ON intake.id=link.intake_id
		JOIN LATERAL (SELECT raw_data FROM sanction_intake_revisions WHERE intake_id=intake.id ORDER BY revision DESC LIMIT 1) revision ON TRUE
		WHERE link.case_id=$1 AND link.relationship<>'duplicate'
		ORDER BY CASE link.relationship WHEN 'primary' THEN 0 ELSE 1 END,link.id LIMIT 1`, caseID).Scan(&intakeID, &raw)
	if err != nil {
		return ""
	}
	return adminCaseSourceReportHTML(adminCaseSourceReportFromRaw(intakeID, raw))
}
