package httpserver

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type umpireRow struct {
	ID       int32
	FullName string
}

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
func (s *Server) renderGMCLFormWithChooser(w io.Writer, seasonID int32, csrfToken, clubName, teamName, captainName, captainEmail, submitterName, submitterEmail, submitterRole, defaultDate string, draft map[string]any, umpires []umpireRow, fixtureChooser string) {
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

	cfg := s.loadCaptainFormConfigForRender(seasonID)

	fmt.Fprint(w, `<div class="container">
<h2 class="mb-3">`+escapeHTML(cfg.Title)+`</h2>
`+renderCaptainFormIntroHTML(cfg.IntroText)+`
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
	if fixtureChooser != "" {
		fmt.Fprint(w, fixtureChooser)
	}
	if formVal(draft, "prefill_source") == "league_api" {
		fmt.Fprint(w, `<div class="alert alert-secondary mb-3">Umpire names were prefilled from the league fixture feed. Please confirm they match your match.</div>`)
	}
	fmt.Fprint(w, `<div id="form-validation-alert" class="alert alert-danger d-none" role="alert">
Please complete all required fields before submitting. The page will scroll to the first missing answer.
</div>`)

	// --- Determine which umpire name to pre-select in the dropdown ---
	// If the saved name matches a known umpire, pre-select it; otherwise select "other".
	umpireSelectVal := func(fieldKey string) string {
		name := val(fieldKey)
		if name == "" {
			return ""
		}
		for _, u := range umpires {
			if strings.EqualFold(u.FullName, name) {
				return u.FullName
			}
		}
		return "other"
	}
	umpireOtherVal := func(fieldKey string) string {
		name := val(fieldKey)
		if name == "" {
			return ""
		}
		for _, u := range umpires {
			if strings.EqualFold(u.FullName, name) {
				return "" // it's in the list, no "other" text needed
			}
		}
		return name
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
        <input type="date" class="form-control" name="match_date" id="match-date-input" value="`+matchDateVal+`" max="`+defaultDate+`" required>
      </div>
      <div class="col-md-6">
        <label class="form-label">Your Club *</label>
        <input type="text" class="form-control" value="`+escapeHTML(clubName)+`" readonly>
      </div>
      <div class="col-md-6">
        <label class="form-label">Opposition</label>
        <input type="text" class="form-control" name="opposition" value="`+escapeHTML(val("opposition"))+`" placeholder="e.g. Milnrow CC 2nd XI"`+prefillReadonly(draft, "opposition")+`>
      </div>
      <div class="col-md-6">
        <label class="form-label">Venue</label>
        <input type="text" class="form-control" name="venue" value="`+escapeHTML(val("venue"))+`" placeholder="e.g. Milnrow CC"`+prefillReadonly(draft, "venue")+`>
      </div>
      <div class="col-md-6">
        <label class="form-label">Match Status *</label>
        <select class="form-select" name="match_outcome" id="match-outcome" required>
          <option value="played"` + selStr(val("match_outcome"), "played") + `>Played</option>
          <option value="play_started_abandoned"` + selStr(val("match_outcome"), "play_started_abandoned") + `>Play Started - Match Abandoned</option>
          <option value="no_play"` + selStr(val("match_outcome"), "no_play") + `>No Play</option>
          <option value="conceded_other_team"` + selStr(val("match_outcome"), "conceded_other_team") + `>Conceded - Other Team</option>
          <option value="conceded_our_team"` + selStr(val("match_outcome"), "conceded_our_team") + `>Conceded - Our Team</option>
        </select>
      </div>
    </div>

    <div id="played-match-fields">
    <div class="row g-3 mt-1">
`)

	// Umpire 1 dropdown
	u1sel := umpireSelectVal("umpire1_name")
	u1other := umpireOtherVal("umpire1_name")
	u1otherDisplay := `style="display:none"`
	if u1sel == "other" {
		u1otherDisplay = ""
	}
	fmt.Fprint(w, `      <div class="col-md-6">
        <label class="form-label">Umpire 1 *</label>
        <div class="d-flex gap-3 mb-2">
          <div class="form-check">
            <input class="form-check-input" type="radio" name="umpire1_type" value="panel" id="u1_panel"`+rad("umpire1_type", "panel")+` onchange="syncUmpireType('1')">
            <label class="form-check-label" for="u1_panel">Panel umpire</label>
          </div>
          <div class="form-check">
            <input class="form-check-input" type="radio" name="umpire1_type" value="club" id="u1_club"`+rad("umpire1_type", "club")+` onchange="syncUmpireType('1')">
            <label class="form-check-label" for="u1_club">Club umpire</label>
          </div>
        </div>
        <select class="form-select mb-2" name="umpire1_name_select" id="umpire1-select" data-played-required="true" required onchange="syncUmpireOther('1')">
          <option value="">-- Select umpire --</option>
`)
	for _, u := range umpires {
		sel := ""
		if u.FullName == u1sel {
			sel = " selected"
		}
		fmt.Fprintf(w, `          <option value="%s"%s>%s</option>`+"\n", escapeHTML(u.FullName), sel, escapeHTML(u.FullName))
	}
	otherSel1 := ""
	if u1sel == "other" {
		otherSel1 = " selected"
	}
	fmt.Fprintf(w, `          <option value="other"%s>Other / not listed</option>
        </select>
        <input type="text" class="form-control mb-2" name="umpire1_name_other" id="umpire1-other" placeholder="Enter umpire name" value="%s" %s>
      </div>
`, otherSel1, escapeHTML(u1other), u1otherDisplay)

	// Umpire 2 dropdown
	u2sel := umpireSelectVal("umpire2_name")
	u2other := umpireOtherVal("umpire2_name")
	u2otherDisplay := `style="display:none"`
	if u2sel == "other" {
		u2otherDisplay = ""
	}
	fmt.Fprint(w, `      <div class="col-md-6">
        <label class="form-label">Umpire 2 *</label>
        <div class="d-flex gap-3 mb-2">
          <div class="form-check">
            <input class="form-check-input" type="radio" name="umpire2_type" value="panel" id="u2_panel"`+rad("umpire2_type", "panel")+` onchange="syncUmpireType('2')">
            <label class="form-check-label" for="u2_panel">Panel umpire</label>
          </div>
          <div class="form-check">
            <input class="form-check-input" type="radio" name="umpire2_type" value="club" id="u2_club"`+rad("umpire2_type", "club")+` onchange="syncUmpireType('2')">
            <label class="form-check-label" for="u2_club">Club umpire</label>
          </div>
        </div>
        <select class="form-select mb-2" name="umpire2_name_select" id="umpire2-select" data-played-required="true" required onchange="syncUmpireOther('2')">
          <option value="">-- Select umpire --</option>
`)
	for _, u := range umpires {
		sel := ""
		if u.FullName == u2sel {
			sel = " selected"
		}
		fmt.Fprintf(w, `          <option value="%s"%s>%s</option>`+"\n", escapeHTML(u.FullName), sel, escapeHTML(u.FullName))
	}
	otherSel2 := ""
	if u2sel == "other" {
		otherSel2 = " selected"
	}
	fmt.Fprintf(w, `          <option value="other"%s>Other / not listed</option>
        </select>
        <input type="text" class="form-control mb-2" name="umpire2_name_other" id="umpire2-other" placeholder="Enter umpire name" value="%s" %s>
      </div>
`, otherSel2, escapeHTML(u2other), u2otherDisplay)

	fmt.Fprintf(w, `      <div class="col-md-4">
        <label class="form-label">Your Team *</label>
        <input type="hidden" name="your_team" data-played-required="true" value="%s">
        <input type="text" class="form-control" value="%s" readonly>
      </div>
      <div class="col-md-4">
        <label class="form-label">Your Name *</label>
        <input type="text" class="form-control" value="%s" readonly>
      </div>
      <div class="col-md-4">
        <label class="form-label">Your Email *</label>
        <input type="email" class="form-control" value="%s" readonly>
      </div>
    </div>

    <hr>
    <div class="row g-3 mb-4">
      <div class="col-md-6">
        <label class="form-label d-block">Did Umpire 1 attend the toss? *</label>
        <div class="form-check form-check-inline">
          <input class="form-check-input" type="radio" name="umpire1_toss_attended" value="Yes" id="u1toss_yes"%s data-played-required="true" required>
          <label class="form-check-label" for="u1toss_yes">Yes</label>
        </div>
        <div class="form-check form-check-inline">
          <input class="form-check-input" type="radio" name="umpire1_toss_attended" value="No" id="u1toss_no"%s>
          <label class="form-check-label" for="u1toss_no">No</label>
        </div>
      </div>
      <div class="col-md-6">
        <label class="form-label d-block">Did Umpire 2 attend the toss? *</label>
        <div class="form-check form-check-inline">
          <input class="form-check-input" type="radio" name="umpire2_toss_attended" value="Yes" id="u2toss_yes"%s data-played-required="true" required>
          <label class="form-check-label" for="u2toss_yes">Yes</label>
        </div>
        <div class="form-check form-check-inline">
          <input class="form-check-input" type="radio" name="umpire2_toss_attended" value="No" id="u2toss_no"%s>
          <label class="form-check-label" for="u2toss_no">No</label>
        </div>
      </div>
    </div>
`,
		escapeHTML(teamName), escapeHTML(teamName), escapeHTML(submitterName), escapeHTML(submitterEmail),
		rad("umpire1_toss_attended", "Yes"), rad("umpire1_toss_attended", "No"),
		rad("umpire2_toss_attended", "Yes"), rad("umpire2_toss_attended", "No"))

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
  <input class="form-check-input" type="radio" name="%s_umpire1" value="%s" id="%s_u1_%s"%s data-played-required="true" required>
  <label class="form-check-label" for="%s_u1_%s">%s</label>
</div>`, fieldKey, score, fieldKey, score, rad(fieldKey+"_umpire1", score), fieldKey, score, score)
		}
		fmt.Fprint(w, `</div><div class="col-md-6"><label class="form-label d-block">Umpire 2 *</label>`)
		for _, score := range []string{"5", "4", "3", "2", "1"} {
			fmt.Fprintf(w, `<div class="form-check form-check-inline">
  <input class="form-check-input" type="radio" name="%s_umpire2" value="%s" id="%s_u2_%s"%s data-played-required="true" required>
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
</div>
`)

	// Section 2 - Pitch & Ground (6 levels each)
	section2Select := func(name, title string, options []struct {
		Val   int
		Label string
	}) {
		fmt.Fprintf(w, `<div class="col-md-6 mb-3">
  <label class="form-label">%s *</label>
  <select class="form-select" name="%s" data-played-required="true" required>
`, title, name)
		for _, o := range options {
			sel := ""
			if formValInt(draft, name) == o.Val {
				sel = " selected"
			}
			fmt.Fprintf(w, `    <option value="%d"%s>%s</option>`+"\n", o.Val, sel, o.Label)
		}
		fmt.Fprint(w, "  </select>\n</div>\n")
	}

	fmt.Fprint(w, `
<div id="played-section2">
<div class="card card-gmcl shadow-sm mb-4">
  <div class="card-header bg-gmcl text-white"><strong>Section 2 &ndash; Scores for the Pitch &amp; Ground</strong></div>
  <div class="card-body">
    <p class="text-muted">ECB requirement: comment on pitch and ground. Criteria are unevenness of bounce, seam movement, carry/bounce and turn from the protected area.</p>
    <div class="row">
`)
	section2Select("unevenness_of_bounce", "Unevenness of Bounce", []struct {
		Val   int
		Label string
	}{
		{1, "No unevenness of bounce at any stage throughout the match"},
		{2, "Little unevenness of bounce at any stage throughout the match"},
		{3, "Occasional, at most, unevenness of bounce throughout the match"},
		{4, "More than occasional, at most, unevenness of bounce throughout the match"},
		{5, "Excessive unevenness of bounce throughout the match"},
		{6, "Unfit, should only be marked as such if dangerous."},
	})
	section2Select("seam_movement", "Seam Movement", []struct {
		Val   int
		Label string
	}{
		{1, "Limited seam movement, at most, at all stages of the match"},
		{2, "Limited seam movement at all stages of the match"},
		{3, "Occasional seam movement at all stages of the match"},
		{4, "More than occasional seam movement at all stages of the match"},
		{5, "Excessive seam movement at all stages of the match"},
		{6, "Unfit, should only be marked as such if dangerous."},
	})
	section2Select("carry_bounce", "Carry and/or Bounce", []struct {
		Val   int
		Label string
	}{
		{1, "Good carry and / or bounce throughout the match"},
		{2, "Average carry and / or bounce throughout the match"},
		{3, "Lacking in carry and / or bounce throughout the match"},
		{4, "Minimal carry and / or bounce throughout the match"},
		{5, "Very minimal carry and / or bounce throughout the match"},
		{6, "Unfit, should only be marked as such if dangerous."},
	})
	section2Select("turn", "Turn", []struct {
		Val   int
		Label string
	}{
		{1, "Little or no turn from the protected area"},
		{2, "A little turn from the protected area"},
		{3, "Moderate turn from the protected area"},
		{4, "Considerable turn from the protected area"},
		{5, "Excessive assistance to spin bowlers from the protected area"},
		{6, "Unfit, should only be marked as such if dangerous."},
	})

	fmt.Fprint(w, `    </div>
  </div>
</div>
</div>
`)

	fmt.Fprint(w, `
<div class="d-grid mb-4">
  <button type="submit" class="btn btn-primary btn-lg">Submit report</button>
</div>
</form>
<p class="text-muted text-center mb-5">Submitting will take you to a separate confirmation page.</p>
</div>
<script>
function syncUmpireOther(n) {
  var sel   = document.getElementById('umpire' + n + '-select');
  var other = document.getElementById('umpire' + n + '-other');
  if (!sel || !other) return;
  if (sel.value === 'other') {
    other.style.display = '';
    other.required = true;
    other.focus();
  } else {
    other.style.display = 'none';
    other.required = false;
    other.value = '';
  }
}

function syncUmpireType(n) {
  var clubRadio = document.getElementById('u' + n + '_club');
  if (!clubRadio || !clubRadio.checked) return;
  // Club umpire: auto-select "Other" so the name field appears
  var sel = document.getElementById('umpire' + n + '-select');
  if (sel && sel.value !== 'other') {
    sel.value = 'other';
    syncUmpireOther(n);
  }
}

(function() {
  var form          = document.getElementById('feedback-form');
  var alertBox      = document.getElementById('form-validation-alert');
  var matchOutcome  = document.getElementById('match-outcome');
  var playedFields  = document.getElementById('played-match-fields');
  var section2      = document.getElementById('played-section2');
  if (!form || !alertBox) return;

  // Outcomes that show the full umpire + pitch questionnaire
  var fullOutcomes = {'played': true, 'play_started_abandoned': true};

  function syncMatchOutcomeState() {
    var outcome = matchOutcome ? matchOutcome.value : 'played';
    var showFull = !!fullOutcomes[outcome];
    if (playedFields) {
      playedFields.style.display = showFull ? '' : 'none';
    }
    if (section2) {
      section2.style.display = showFull ? '' : 'none';
    }
    form.querySelectorAll('[data-played-required="true"]').forEach(function(field) {
      field.required = showFull;
    });
    // Keep umpire-other fields in sync with their selects when showing
    if (showFull) {
      syncUmpireOther('1');
      syncUmpireOther('2');
    }
  }

  function showValidationMessage() {
    alertBox.classList.remove('d-none');
    var invalid = form.querySelector(':invalid');
    if (invalid) {
      invalid.scrollIntoView({ behavior: 'smooth', block: 'center' });
      try { invalid.focus({ preventScroll: true }); } catch (_) { invalid.focus(); }
    }
  }

  form.addEventListener('submit', function(e) {
    syncMatchOutcomeState();
    if (!form.checkValidity()) {
      e.preventDefault();
      showValidationMessage();
      form.reportValidity();
    }
  });

  form.addEventListener('input', function() {
    if (form.checkValidity()) {
      alertBox.classList.add('d-none');
    }
  });
  if (matchOutcome) {
    matchOutcome.addEventListener('change', syncMatchOutcomeState);
    syncMatchOutcomeState();
  }
})();
</script>
`)
	pageFooter(w)
}

// prefillReadonly returns " readonly" when a field was pre-filled from the league API.
func prefillReadonly(draft map[string]any, field string) string {
	if formVal(draft, "prefill_source") == "league_api" && formVal(draft, field) != "" {
		return " readonly"
	}
	return ""
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

func (s *Server) loadCaptainFormConfigForRender(seasonID int32) captainFormConfig {
	cfg := captainFormConfig{
		SeasonID:  seasonID,
		Title:     "GMCL Captain's Report - Prem 1, Prem 2, Championship & Division 1",
		IntroText: "This form is for you to comment on:\n\n1. The performance of the umpires at your game for GMCLUA.\n2. The pitch and ground conditions to meet ECB requirements.\n\nComments about league playing regulations should go to rules@gtrmcrcricket.co.uk.",
	}
	if seasonID == 0 {
		return cfg
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	loaded, err := s.loadCaptainFormConfig(ctx, seasonID)
	if err == nil && loaded.Title != "" && loaded.IntroText != "" {
		cfg = loaded
	}
	return cfg
}

func renderCaptainFormIntroHTML(introText string) string {
	parts := strings.Split(strings.TrimSpace(introText), "\n\n")
	var b strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		b.WriteString(`<div class="alert alert-light border mb-4"><div class="mb-0">`)
		b.WriteString(strings.ReplaceAll(escapeHTML(part), "\n", "<br>"))
		b.WriteString(`</div></div>`)
	}
	return b.String()
}
