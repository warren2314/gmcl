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

// renderGMCLForm writes the 2025 GMCL Captain's Report questionnaire (umpires + pitch/ground).
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
<h2 class="mb-3">2025 GMCL Captain's Report</h2>
<p class="text-muted">Saturday Lower Division (2,3,4,5,6 including Club Umpires) and All Sunday Divisions.</p>
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
    <p class="text-muted">Please provide constructive feedback. Give the date of the match, your club name and the umpires standing.</p>
    <p><strong>IMPORTANT:</strong> Mandatory to provide comments when giving a score of 2 or below.</p>
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
        <select class="form-select" name="your_team" required>
          <option value="1st XI"`+selStr(val("your_team"), "1st XI")+`>1st XI</option>
          <option value="2nd XI"`+selStr(val("your_team"), "2nd XI")+`>2nd XI</option>
          <option value="3rd XI"`+selStr(val("your_team"), "3rd XI")+`>3rd XI</option>
          <option value="4th XI"`+selStr(val("your_team"), "4th XI")+`>4th XI</option>
          <option value="5th XI"`+selStr(val("your_team"), "5th XI")+`>5th XI</option>
        </select>
      </div>
      <div class="col-md-4">
        <label class="form-label">Your Name *</label>
        <input type="text" class="form-control" value="`+escapeHTML(submitterName)+`" readonly>
      </div>
      <div class="col-md-4">
        <label class="form-label">Your Email *</label>
        <input type="email" class="form-control" value="`+escapeHTML(submitterEmail)+`" readonly>
      </div>
      <div class="col-md-6">
        <label class="form-label">Umpire 1 Type *</label>
        <select class="form-select" name="umpire1_type" required>
          <option value="panel"`+selStr(val("umpire1_type"), "panel")+`>Panel umpire</option>
          <option value="club"`+selStr(val("umpire1_type"), "club")+`>Club umpire</option>
        </select>
      </div>
      <div class="col-md-6">
        <label class="form-label">Umpire 2 Type *</label>
        <select class="form-select" name="umpire2_type" required>
          <option value="panel"`+selStr(val("umpire2_type"), "panel")+`>Panel umpire</option>
          <option value="club"`+selStr(val("umpire2_type"), "club")+`>Club umpire</option>
        </select>
      </div>
    </div>

    <hr>
    <h6>Umpire Performance (Poor / Average / Good)</h6>
    <div class="mb-3">
      <label class="form-label">Umpire 1:</label><br>
      <div class="form-check form-check-inline">
        <input class="form-check-input" type="radio" name="umpire1_performance" value="Poor" id="u1p"`+rad("umpire1_performance", "Poor")+`>
        <label class="form-check-label" for="u1p">Poor</label>
      </div>
      <div class="form-check form-check-inline">
        <input class="form-check-input" type="radio" name="umpire1_performance" value="Average" id="u1a"`+rad("umpire1_performance", "Average")+`>
        <label class="form-check-label" for="u1a">Average</label>
      </div>
      <div class="form-check form-check-inline">
        <input class="form-check-input" type="radio" name="umpire1_performance" value="Good" id="u1g"`+rad("umpire1_performance", "Good")+`>
        <label class="form-check-label" for="u1g">Good</label>
      </div>
    </div>
    <div class="mb-3">
      <label class="form-label">Umpire 2:</label><br>
      <div class="form-check form-check-inline">
        <input class="form-check-input" type="radio" name="umpire2_performance" value="Poor" id="u2p"`+rad("umpire2_performance", "Poor")+`>
        <label class="form-check-label" for="u2p">Poor</label>
      </div>
      <div class="form-check form-check-inline">
        <input class="form-check-input" type="radio" name="umpire2_performance" value="Average" id="u2a"`+rad("umpire2_performance", "Average")+`>
        <label class="form-check-label" for="u2a">Average</label>
      </div>
      <div class="form-check form-check-inline">
        <input class="form-check-input" type="radio" name="umpire2_performance" value="Good" id="u2g"`+rad("umpire2_performance", "Good")+`>
        <label class="form-check-label" for="u2g">Good</label>
      </div>
    </div>

    <div class="mb-3">
      <label class="form-label">Comments re Umpire 1 &amp; 2 (performance only):</label>
      <textarea class="form-control" name="umpire_comments" rows="4" hx-post="/captain/form/autosave" hx-trigger="keyup changed delay:800ms" hx-target="#autosave-status" hx-include="closest form" hx-swap="innerHTML">`+escapeHTML(val("umpire_comments"))+`</textarea>
    </div>
    <div class="mb-3">
      <label class="form-label">Detailed feedback (optional):</label>
      <textarea class="form-control" name="umpire_comments_detail" rows="3" hx-post="/captain/form/autosave" hx-trigger="keyup changed delay:800ms" hx-target="#autosave-status" hx-include="closest form" hx-swap="innerHTML">`+escapeHTML(val("umpire_comments_detail"))+`</textarea>
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
		{1, "No unevenness / Very good"},
		{2, "Little unevenness / Good"},
		{3, "Occasional / Above average"},
		{4, "More than occasional / Below average"},
		{5, "Excessive / Poor"},
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
    <p class="text-muted">ECB requirement: comment on pitch and ground. Criteria: Unevenness of Bounce, Seam Movement, Carry and/or Bounce, Turn (from protected area).</p>
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
