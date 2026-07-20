package httpserver

import (
	"fmt"
)

// starredPopoverInitScript activates every Bootstrap help popover on the page.
// It must run after the Bootstrap bundle, so pass it to pageFooterWithScript.
const starredPopoverInitScript = `document.querySelectorAll('[data-bs-toggle="popover"]').forEach(function (el) { new bootstrap.Popover(el); });`

// starredHelpIcon renders the small "?" button shown beside a heading. The
// popover explains what the section is and why its numbers matter, so the
// page can be used without knowing Rule 3.5 by heart. Dismisses on blur.
func starredHelpIcon(title, body string) string {
	return fmt.Sprintf(`<button type="button" class="btn btn-link p-0 ms-2 align-baseline" data-bs-toggle="popover" data-bs-trigger="focus" data-bs-placement="auto" data-bs-title="%s" data-bs-content="%s" aria-label="Help: %s"><span class="badge rounded-circle text-bg-secondary" style="width:1.2rem;height:1.2rem;padding:0;line-height:1.2rem;font-size:.72rem">?</span></button>`, escapeHTML(title), escapeHTML(body), escapeHTML(title))
}

// starredSectionTitle renders a card-header heading with an optional workflow
// step badge, a help icon, and a one-line subtitle underneath.
func starredSectionTitle(step, title, subtitle, helpTitle, helpBody string) string {
	badge := ""
	if step != "" {
		badge = `<span class="badge text-bg-dark me-2">` + escapeHTML(step) + `</span>`
	}
	sub := ""
	if subtitle != "" {
		sub = `<div class="small text-muted">` + subtitle + `</div>`
	}
	return fmt.Sprintf(`<div class="fw-semibold">%s%s%s</div>%s`, badge, escapeHTML(title), starredHelpIcon(helpTitle, helpBody), sub)
}

// starredNavPill renders one sticky-nav jump link. count > 0 shows how many
// items are outstanding; count == 0 shows a green check; count < 0 hides the
// badge entirely. label may contain trusted HTML entities.
func starredNavPill(href, label string, count int, badgeClass string) string {
	badge := ""
	if count == 0 {
		badge = ` <span class="badge text-bg-success">&#10003;</span>`
	} else if count > 0 {
		badge = fmt.Sprintf(` <span class="badge %s">%d</span>`, badgeClass, count)
	}
	return fmt.Sprintf(`<a class="btn btn-sm btn-outline-primary" href="%s">%s%s</a>`, href, label, badge)
}

// starredDataTileHTML renders one "imported data" overview tile: a count of
// source data with a link into the matching detail view.
func starredDataTileHTML(label, helpTitle, helpBody, valueHTML, subHTML, href, linkText string) string {
	sub := ""
	if subHTML != "" {
		sub = `<div class="small text-muted">` + subHTML + `</div>`
	}
	return fmt.Sprintf(`<div class="col"><div class="border rounded p-2 h-100 d-flex flex-column"><div class="small text-muted">%s%s</div><div class="fs-3 fw-semibold">%s</div>%s<a class="small mt-auto" href="%s">%s &rarr;</a></div></div>`, escapeHTML(label), starredHelpIcon(helpTitle, helpBody), valueHTML, sub, href, escapeHTML(linkText))
}

// starredActionTileHTML renders one "action queue" tile. A zero count turns
// the tile green so an admin can see at a glance that nothing is outstanding.
func starredActionTileHTML(count int, label, helpTitle, helpBody, href, actionText, accentClass string) string {
	if count == 0 {
		return fmt.Sprintf(`<div class="col"><div class="border border-success rounded p-2 h-100 d-flex flex-column"><div class="small text-muted">%s%s</div><div class="fs-3 fw-semibold text-success">&#10003;</div><div class="small text-success mt-auto">All clear</div></div></div>`, escapeHTML(label), starredHelpIcon(helpTitle, helpBody))
	}
	return fmt.Sprintf(`<div class="col"><div class="border border-%s rounded p-2 h-100 d-flex flex-column"><div class="small text-muted">%s%s</div><div class="fs-3 fw-semibold text-%s">%d</div><a class="small mt-auto" href="%s">%s &rarr;</a></div></div>`, accentClass, escapeHTML(label), starredHelpIcon(helpTitle, helpBody), accentClass, count, href, escapeHTML(actionText))
}
