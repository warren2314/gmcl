package httpserver

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	sanctiondomain "cricket-ground-feedback/internal/sanctions"
)

type adminDecisionSeason struct {
	id         int32
	name       string
	start, end time.Time
}

func (s *Server) loadAdminDecisionSeasons(ctx context.Context, caseID int64) []adminDecisionSeason {
	rows, err := s.DB.Query(ctx, `SELECT target.id,target.name,target.start_date,target.end_date
		FROM seasons target JOIN sanction_cases c ON c.id=$1
		JOIN seasons original ON original.id=c.season_id
		WHERE target.start_date>original.start_date AND NOT target.is_archived
		ORDER BY target.start_date,target.id`, caseID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var seasons []adminDecisionSeason
	for rows.Next() {
		var season adminDecisionSeason
		if rows.Scan(&season.id, &season.name, &season.start, &season.end) != nil {
			return nil
		}
		seasons = append(seasons, season)
	}
	if rows.Err() != nil {
		return nil
	}
	return seasons
}

func adminScheduledCardFieldsHTML(index int, seasons []adminDecisionSeason) string {
	var out strings.Builder
	fmt.Fprintf(&out, `<div class="col-12" data-card-schedule><div class="alert alert-info mb-0"><strong>Choose how these cards take effect.</strong> Future-season cards definitely apply on the effective date. Suspended cards apply only after the recorded condition is breached and an activation is separately authorised. A date alone never activates a conditional suspension.</div></div><div class="col-md-4" data-card-schedule><label class="form-label" for="red-card-count-%d">Number of red cards</label><input id="red-card-count-%d" class="form-control" name="red_card_count" type="number" min="1" max="100" step="1" value="1"><div class="form-text">Enter the total number of cards awarded.</div></div>`, index, index)
	fmt.Fprintf(&out, `<div class="col-md-4" data-card-schedule><label class="form-label" for="target-season-%d">Applies in season</label><select id="target-season-%d" class="form-select" name="target_season_id"><option value="">Select future season (or case season for a suspension)</option>`, index, index)
	for _, season := range seasons {
		fmt.Fprintf(&out, `<option value="%d" data-start="%s" data-end="%s">%s</option>`, season.id, season.start.Format("2006-01-02"), season.end.Format("2006-01-02"), escapeHTML(season.name))
	}
	fmt.Fprint(&out, `</select>`)
	if len(seasons) == 0 {
		fmt.Fprint(&out, `<div class="form-text text-danger">No future season is configured. Ask the league administrator to configure the target season before saving a future-season award.</div>`)
	}
	fmt.Fprintf(&out, `</div><div class="col-md-4" data-card-schedule><label class="form-label" for="card-starts-%d">Effective from</label><input id="card-starts-%d" class="form-control" name="starts_at" type="date"><div class="form-text">Selecting a season fills its start date. Check this against the agreed decision.</div></div><div class="col-12" data-card-schedule><p class="small text-muted mb-0" data-card-points-guidance>Points are calculated against the selected season's red-card total when you save. Three reds starting from zero give 1 + 2 + 3 = 6 points. Do not add another points adjustment for the same card deduction. Denver's task cannot be completed before the effective date.</p></div>`, index, index)
	return out.String()
}

// Keep all row fields submitted, including hidden ones, so optional blank rows
// cannot shift a later effect's season/date/count onto a different subject.
const adminScheduledCardFormScript = `<script>(()=>{
document.querySelectorAll('[data-decision-effect]').forEach(row=>{
 const type=row.querySelector('[name=effect_type]');
 const season=row.querySelector('[name=target_season_id]');
 const date=row.querySelector('[name=starts_at]');
 const count=row.querySelector('[name=red_card_count]');
 const trigger=row.querySelector('[name=trigger_condition]');
 const guidance=row.querySelector('[data-card-points-guidance]');
 const definiteGuidance=guidance.textContent;
 const update=()=>{
  const scheduled=type.value==='scheduled_red', conditional=type.value==='suspended_red';
  row.querySelectorAll('[data-card-schedule]').forEach(el=>el.hidden=!(scheduled||conditional));
  ['fine_pounds','points','rescindable','ends_at','trigger_condition'].forEach(name=>{
   const field=row.querySelector('[name='+name+']');
   const allowed=conditional&&(name==='ends_at'||name==='trigger_condition');
   field.parentElement.hidden=(scheduled||conditional)&&!allowed;
  });
  guidance.textContent=conditional?'Conditional cards add no cards or points until activation is separately authorised. Record the condition below.':definiteGuidance;
  season.required=scheduled; count.required=scheduled||conditional;
  date.required=scheduled||(conditional&&season.value!=='');
  trigger.required=conditional&&season.value!=='';
  trigger.parentElement.querySelector('.text-muted').textContent=trigger.required?'(required)':'(optional)';
  const option=season.selectedOptions[0];
  if((scheduled||conditional)&&option&&option.dataset.start){date.min=option.dataset.start;date.max=option.dataset.end;}
  else{date.removeAttribute('min');date.removeAttribute('max');}
 };
 type.addEventListener('change',()=>{if(type.value==='scheduled_red')trigger.value='';update();});
 season.addEventListener('change',()=>{const option=season.selectedOptions[0];date.value=option&&option.dataset.start||'';update();});
 update();
});
})();</script>`

// Parse scheduling strictly: a malformed season/date must never silently fall
// back to the case's current season. Existing unscheduled effects retain their
// original field parsing behavior.
func parseAdminDecisionEffectsWithSchedule(form url.Values) ([]sanctiondomain.DecisionEffectRequest, error) {
	effects := parseAdminDecisionEffects(form)
	at := func(name string, index int) string {
		if index >= len(form[name]) {
			return ""
		}
		return strings.TrimSpace(form[name][index])
	}
	effectIndex := 0
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		return nil, fmt.Errorf("load league timezone: %w", err)
	}
	for index, value := range form["effect_type"] {
		kind := strings.TrimSpace(value)
		if kind == "" {
			continue
		}
		effect := &effects[effectIndex]
		effectIndex++
		if kind != "scheduled_red" && kind != "suspended_red" {
			continue
		}
		countText := at("red_card_count", index)
		if countText == "" && kind == "suspended_red" {
			countText = "1"
		}
		count, countErr := strconv.Atoi(countText)
		if countErr != nil || count < 1 || count > 100 {
			return nil, fmt.Errorf("effect %d: enter a whole number of red cards from 1 to 100", index+1)
		}
		effect.RedCardCount = count
		seasonText := at("target_season_id", index)
		if seasonText != "" {
			season, seasonErr := strconv.ParseInt(seasonText, 10, 32)
			if seasonErr != nil || season < 1 {
				return nil, fmt.Errorf("effect %d: select a valid target season", index+1)
			}
			id := int32(season)
			effect.TargetSeasonID = &id
		} else if kind == "scheduled_red" {
			return nil, fmt.Errorf("effect %d: select the future season for these cards", index+1)
		}
		dateText := at("starts_at", index)
		if dateText != "" {
			date, dateErr := time.ParseInLocation("2006-01-02", dateText, loc)
			if dateErr != nil {
				return nil, fmt.Errorf("effect %d: enter a valid effective date", index+1)
			}
			effect.StartsAt = &date
		} else if kind == "scheduled_red" || effect.TargetSeasonID != nil {
			return nil, fmt.Errorf("effect %d: enter the effective date for the future season", index+1)
		}
		if endText := at("ends_at", index); endText != "" && kind == "suspended_red" {
			end, endErr := time.ParseInLocation("2006-01-02", endText, loc)
			if endErr != nil {
				return nil, fmt.Errorf("effect %d: enter a valid suspension end date", index+1)
			}
			effect.EndsAt = &end
		}
		if kind == "scheduled_red" && effect.Trigger != "" {
			return nil, fmt.Errorf("effect %d: definite future cards must not have an activation condition; choose suspended red cards for a conditional award", index+1)
		}
		if kind == "suspended_red" && effect.TargetSeasonID != nil && effect.Trigger == "" {
			return nil, fmt.Errorf("effect %d: record the condition that would activate these suspended cards", index+1)
		}
	}
	return effects, nil
}
