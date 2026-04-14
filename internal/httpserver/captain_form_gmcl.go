package httpserver

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func formVal(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func formValInt(m map[string]any, k string) int {
	if v, ok := m[k]; ok && v != nil {
		switch x := v.(type) {
		case float64:
			return int(x)
		case int:
			return x
		case string:
			i, _ := strconv.Atoi(x)
			return i
		}
	}
	return 0
}

func selStr(current, want string) string {
	if current == want {
		return " selected"
	}
	return ""
}

// renderGMCLForm writes the GMCL Captain's Report questionnaire.
func (s *Server) renderGMCLForm(w io.Writer, csrfToken, clubName, teamName, captainName, captainEmail, submitterName, submitterEmail, submitterRole, defaultDate string, draft map[string]any) {
	val := func(k string) string { return formVal(draft, k) }
	rad := func(k string, want string) string {
		if formVal(draft, k) == want {
			return " checked"
		}
		return ""
	}
	matchDateVal := val("match_date")
	if matchDateVal == "" {
		matchDateVal = defaultDate
	}

	pageHead(w, "Captain's Report")
	writeCaptainNav(w)

	fmt.Fprint(w, `<div class="container">
<h2 class="mb-3">GMCL Captain's Report - Prem 1, Prem 2, Championship &amp; Division 1</h2>
<p class="text-muted">This form covers umpire performance and the ECB-required pitch and ground feedback for your match.</p>
<div class="alert alert-light border mb-4">
  <div>This form is for you to comment on:</div>
  <ol class="mb-2">
    <li>The performance of the umpires at your game for GMCLUA.</li>
    <li>The pitch and ground conditions to meet ECB requirements.</li>
  </ol>
  <div class="small text-muted">Comments about league playing regulations should go to <a href="mailto:rules@gtrmcrcricket.co.uk">rules@gtrmcricket.co.uk</a>.</div>
</div>
`)
	if submitterRole == "delegate" {
		fmt.Fprintf(
			w,
			`<div class="alert alert-info">You are submitting as stand-in captain (%s) on behalf of %s.</div>`,
			escapeHTML(submitterEmail),
			escapeHTML(captainName),
		)
	} else {
		fmt.Fprint(w, `<div class="card card-gmcl shadow-sm mb-4">
  <div class="card-header bg-gmcl text-white"><strong>Stand-in captain (this week)</strong></div>
  <div class="card-body">
    <p class="text-muted mb-3">If needed, invite a stand-in captain for this week only. Invites are sent by `+escapeHTML(captainEmail)+`.</p>
    <form method="POST" action="/captain/delegate/invite" class="row g-2">
      <input type="hidden" name="csrf_token" value="`+csrfToken+`">
      <div class="col-md-5">
        <input type="text" class="form-control" name="delegate_name" placeholder="Stand-in name (optional)">
      </div>
      <div class="col-md-5">
        <input type="email" class="form-control" name="delegate_email" placeholder="Stand-in email" required>
      </div>
      <div class="col-md-2 d-grid">
        <button type="submit" class="btn btn-outline-primary">Send invite</button>
      </div>
    </form>
  </div>
</div>`)
	}
	fmt.Fprint(w, `
<form id="feedback-form" method="POST" action="/captain/form/submit">
  <input type="hidden" name="csrf_token" value="`+csrfToken+`">
`)
	if formVal(draft, "prefill_source") == "league_api" {
		fmt.Fprint(w, `<div class="alert alert-secondary mb-3">Umpire names were prefilled from the league fixture feed. Please confirm they match your match.</div>`)
	}

	// Section 1 - Umpires
	fmt.Fprint(w, `
<div class="card card-gmcl shadow-sm mb-4">
  <div class="card-header bg-gmcl text-white"><strong>Section 1 &ndash; Captain's Report on the Umpires</strong></div>
  <div class="card-body">
    <p class="text-muted">Please provide constructive feedback. If you give a mark of 2 or 1 for an umpire, explain the reasons.</p>
    <div class="row g-3">
      <div class="col-md-6">
        <label class="form-label">Date *</label>
        <input type="date" class="form-control" name="match_date" value="`+matchDateVal+`" required>
      </div>
      <div class="col-md-6">
        <label class="form-label">Your Club *</label>
        <input type="text" class="form-control" value="`+escapeHTML(clubName)+`" readonly>
      </div>
      <div class="col-md-6">
        <label class="form-label">Umpire 1 Name *</label>
        <input type="text" class="form-control" name="umpire1_name" required value="`+escapeHTML(val("umpire1_name"))+`">
      </div>
      <div class="col-md-6">
        <label class="form-label">Umpire 2 Name *</label>
        <input type="text" class="form-control" name="umpire2_name" required value="`+escapeHTML(val("umpire2_name"))+`">
      </div>
      <div class="col-12">
        <label class="form-label">If not listed, enter names below</label>
        <input type="text" class="form-control" name="umpire_names_other" value="`+escapeHTML(val("umpire_names_other"))+`">
      </div>
      <div class="col-md-4">
        <label class="form-label">Your Team *</label>
        <input type="hidden" name="your_team" value="`+escapeHTML(teamName)+`">
        <input type="text" class="form-control" value="`+escapeHTML(teamName)+`" readonly>
      </div>
      <div class="col-md-4">
        <label class="form-label">Your Name *</label>
        <input type="text" class="form-control" value="`+escapeHTML(submitterName)+`" readonly>
      </div>
      <div class="col-md-4">
        <label class="form-label">Your Email *</label>
        <input type="email" class="form-control" value="`+escapeHTML(submitterEmail)+`" readonly>
      </div>
    </div>

    <hr>
    <div class="row g-3 mb-4">
      <div class="col-md-6">
        <label class="form-label d-block">Did Umpire 1 attend the toss? *</label>
        <div class="form-check form-check-inline">
          <input class="form-check-input" type="radio" name="umpire1_toss_attended" value="Yes" id="u1toss_yes"`+rad("umpire1_toss_attended", "Yes")+` required>
          <label class="form-check-label" for="u1toss_yes">Yes</label>
        </div>
        <div class="form-check form-check-inline">
          <input class="form-check-input" type="radio" name="umpire1_toss_attended" value="No" id="u1toss_no"`+rad("umpire1_toss_attended", "No")+`>
          <label class="form-check-label" for="u1toss_no">No</label>
        </div>
      </div>
      <div class="col-md-6">
        <label class="form-label d-block">Did Umpire 2 attend the toss? *</label>
        <div class="form-check form-check-inline">
          <input class="form-check-input" type="radio" name="umpire2_toss_attended" value="Yes" id="u2toss_yes"`+rad("umpire2_toss_attended", "Yes")+` required>
          <label class="form-check-label" for="u2toss_yes">Yes</label>
        </div>
        <div class="form-check form-check-inline">
          <input class="form-check-input" type="radio" name="umpire2_toss_attended" value="No" id="u2toss_no"`+rad("umpire2_toss_attended", "No")+`>
          <label class="form-check-label" for="u2toss_no">No</label>
        </div>
      </div>
    </div>
`)

	scoreMatrix := []struct {
		Key   string
		Title string
		Body  string
	}{
		{"decision_making", "Decision Making", "Umpire appeared calm, well positioned, confident and clear when explaining decision making."},
		{"match_management", "Match Management", "Ensured a safe and positive playing environment and applied the laws and playing conditions accurately."},
		{"player_management", "Player Management", "Worked well with players and captains before, during and after the match and dealt with behaviour challenges early and fairly."},
		{"presence_image", "Presence &amp; Image", "Used communication styles positively with players and captains for the benefit of the game."},
		{"teamwork", "Teamwork", "Showed effective co-operation with officiating colleagues for an effective game."},
	}
	renderScoreRow := func(fieldKey, label, help string) {
		fmt.Fprintf(w, `<div class="card border-0 bg-light mb-3">
  <div class="card-body">
    <div class="fw-semibold mb-1">%s</div>
    <div class="text-muted small mb-3">%s</div>
    <div class="row g-3 align-items-start">
      <div class="col-md-6">
        <label class="form-label d-block">Umpire 1 *</label>
`, label, help)
		for _, score := range []string{"5", "4", "3", "2", "1"} {
			fmt.Fprintf(w, `<div class="form-check form-check-inline">
  <input class="form-check-input" type="radio" name="%s_umpire1" value="%s" id="%s_u1_%s"%s required>
  <label class="form-check-label" for="%s_u1_%s">%s</label>
</div>`, fieldKey, score, fieldKey, score, rad(fieldKey+"_umpire1", score), fieldKey, score, score)
		}
		fmt.Fprint(w, `</div><div class="col-md-6"><label class="form-label d-block">Umpire 2 *</label>`)
		for _, score := range []string{"5", "4", "3", "2", "1"} {
			fmt.Fprintf(w, `<div class="form-check form-check-inline">
  <input class="form-check-input" type="radio" name="%s_umpire2" value="%s" id="%s_u2_%s"%s required>
  <label class="form-check-label" for="%s_u2_%s">%s</label>
</div>`, fieldKey, score, fieldKey, score, rad(fieldKey+"_umpire2", score), fieldKey, score, score)
		}
		fmt.Fprint(w, `</div></div></div></div>`)
	}

	fmt.Fprint(w, `<div class="mb-3 small text-muted"><strong>Scoring guide:</strong> 5 = Outstanding, 4 = Above Standard, 3 = Standard Expected, 2 = Development Needed, 1 = Poor.</div>`)
	for _, row := range scoreMatrix {
		renderScoreRow(row.Key, row.Title, row.Body)
	}

	fmt.Fprint(w, `
    <div class="mb-3">
      <label class="form-label">If you have given a mark of 2 or 1 for Umpire 1, explain the reasons.</label>
      <textarea class="form-control" name="umpire1_reason" rows="4" hx-post="/captain/form/autosave" hx-trigger="keyup changed delay:800ms" hx-target="#autosave-status" hx-include="closest form" hx-swap="innerHTML">`+escapeHTML(val("umpire1_reason"))+`</textarea>
    </div>
    <div class="mb-3">
      <label class="form-label">If you have given a mark of 2 or 1 for Umpire 2, explain the reasons.</label>
      <textarea class="form-control" name="umpire2_reason" rows="4" hx-post="/captain/form/autosave" hx-trigger="keyup changed delay:800ms" hx-target="#autosave-status" hx-include="closest form" hx-swap="innerHTML">`+escapeHTML(val("umpire2_reason"))+`</textarea>
    </div>
    <div id="autosave-status"></div>
  </div>
</div>
`)

	// Section 2 - Pitch & Ground (6 levels each)
	opts := []struct {
		Val   int
		Label string
	}{
		{1, "Very good / minimal concern"},
		{2, "Good"},
		{3, "Above average / acceptable"},
		{4, "Below average"},
		{5, "Poor"},
		{6, "Unfit (dangerous)"},
	}
	section2Select := func(name, title string) {
		fmt.Fprintf(w, `<div class="col-md-6 mb-3">
  <label class="form-label">%s *</label>
  <select class="form-select" name="%s" required>
`, title, name)
		for _, o := range opts {
			sel := ""
			if formValInt(draft, name) == o.Val {
				sel = " selected"
			}
			fmt.Fprintf(w, `    <option value="%d"%s>%s</option>`+"\n", o.Val, sel, o.Label)
		}
		fmt.Fprint(w, "  </select>\n</div>\n")
	}

	fmt.Fprint(w, `
<div class="card card-gmcl shadow-sm mb-4">
  <div class="card-header bg-gmcl text-white"><strong>Section 2 &ndash; Scores for the Pitch &amp; Ground</strong></div>
  <div class="card-body">
    <p class="text-muted">ECB requirement: comment on pitch and ground. Criteria are unevenness of bounce, seam movement, carry/bounce and turn from the protected area.</p>
    <div class="row">
`)
	section2Select("unevenness_of_bounce", "Unevenness of Bounce")
	section2Select("seam_movement", "Seam Movement")
	section2Select("carry_bounce", "Carry and/or Bounce")
	section2Select("turn", "Turn")

	fmt.Fprint(w, `    </div>
  </div>
</div>

<div class="d-grid mb-4">
  <button type="submit" class="btn btn-primary btn-lg">Submit report</button>
</div>
</form>
<p class="text-muted text-center mb-5">Thanks for completing the form. You will receive a copy at the email above.</p>
</div>
`)
	pageFooter(w)
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
