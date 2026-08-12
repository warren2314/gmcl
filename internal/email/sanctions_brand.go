package email

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

var sanctionURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)
var sanctionRulePattern = regexp.MustCompile(`(?i)^rule\s+[0-9]+(?:\.[0-9]+)*\b`)

func sanctionEmailHTML(subject, body string) string {
	var content strings.Builder
	inList := false
	closeList := func() {
		if inList {
			content.WriteString(`</ul>`)
			inList = false
		}
	}

	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			if !inList {
				content.WriteString(`<ul style="margin:8px 0 18px;padding-left:24px">`)
				inList = true
			}
			fmt.Fprintf(&content, `<li style="margin:0 0 7px">%s</li>`, sanctionLinkify(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))))
			continue
		}
		closeList()
		if trimmed == "" {
			continue
		}

		lower := strings.ToLower(trimmed)
		switch {
		case strings.Contains(lower, "/sanctions/case/respond"):
			link := sanctionFirstURL(trimmed)
			if link != "" {
				fmt.Fprintf(&content, `<table role="presentation" cellspacing="0" cellpadding="0" style="margin:22px 0"><tr><td style="border-radius:5px;background:#c41e3a"><a href="%s" style="display:inline-block;padding:13px 24px;color:#ffffff;text-decoration:none;font-weight:bold">Respond securely</a></td></tr></table><p style="margin:0 0 18px;color:#5f6368;font-size:12px;word-break:break-all">If the button does not work, use: <a href="%s" style="color:#9f152f">%s</a></p>`, html.EscapeString(link), html.EscapeString(link), html.EscapeString(link))
				continue
			}
		case strings.HasPrefix(lower, "rule being checked:") || strings.HasPrefix(lower, "rule determination:"):
			fmt.Fprintf(&content, `<p style="margin:20px 0 7px;color:#9f152f;font-size:13px;font-weight:bold;text-transform:uppercase;letter-spacing:.4px">%s</p>`, sanctionLinkify(trimmed))
			continue
		case strings.HasPrefix(lower, "alleged rule under investigation:") || sanctionRulePattern.MatchString(trimmed):
			fmt.Fprintf(&content, `<div style="margin:0 0 16px;padding:14px 16px;background:#fff4d6;border-left:5px solid #c41e3a;color:#2f3033;font-weight:bold">%s</div>`, sanctionLinkify(trimmed))
			continue
		case strings.HasPrefix(lower, "published source:"):
			fmt.Fprintf(&content, `<p style="margin:0 0 18px;padding:0 16px;color:#5f6368;font-size:13px">%s</p>`, sanctionLinkify(trimmed))
			continue
		case strings.HasSuffix(trimmed, ":") && len(trimmed) < 80:
			fmt.Fprintf(&content, `<p style="margin:20px 0 7px;color:#202124;font-weight:bold">%s</p>`, sanctionLinkify(trimmed))
			continue
		default:
			fmt.Fprintf(&content, `<p style="margin:0 0 14px">%s</p>`, sanctionLinkify(trimmed))
		}
	}
	closeList()

	testBanner := ""
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(subject)), "[TEST ONLY") {
		testBanner = `<tr><td style="padding:11px 24px;background:#fff0b3;border-bottom:1px solid #e2c65b;color:#604b00;font-weight:bold;text-align:center">TEST EMAIL — NO CLUB HAS BEEN CONTACTED</td></tr>`
	}

	return `<!DOCTYPE html><html><body style="margin:0;padding:0;background:#f2f3f5;color:#2f3033;font-family:Arial,Helvetica,sans-serif;font-size:15px;line-height:1.55">` +
		`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="background:#f2f3f5"><tr><td align="center" style="padding:24px 12px">` +
		`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:640px;background:#ffffff;border:1px solid #dedfe2;border-radius:8px;overflow:hidden">` +
		`<tr><td style="padding:22px 26px;background:#c41e3a;color:#ffffff"><div style="font-size:28px;line-height:1;font-weight:bold;letter-spacing:.5px">GMCL</div><div style="margin-top:7px;font-size:13px">Greater Manchester Cricket League</div></td></tr>` +
		testBanner +
		`<tr><td style="padding:24px 26px 5px"><h1 style="margin:0;color:#202124;font-size:21px;line-height:1.3">` + html.EscapeString(subject) + `</h1></td></tr>` +
		`<tr><td style="padding:17px 26px 26px">` + content.String() + `</td></tr>` +
		`<tr><td style="padding:18px 26px;background:#f7f7f8;border-top:1px solid #dedfe2;color:#666b70;font-size:12px">Sent by Greater Manchester Cricket League · <a href="https://gmcl.co.uk" style="color:#9f152f">gmcl.co.uk</a><br>This is official case correspondence. Please retain the case reference when contacting the league.</td></tr>` +
		`</table></td></tr></table></body></html>`
}

func sanctionFirstURL(text string) string {
	match := sanctionURLPattern.FindString(text)
	return strings.TrimRight(match, ".,;:!?)]}")
}

func sanctionLinkify(text string) string {
	var out strings.Builder
	last := 0
	for _, loc := range sanctionURLPattern.FindAllStringIndex(text, -1) {
		out.WriteString(html.EscapeString(text[last:loc[0]]))
		raw := text[loc[0]:loc[1]]
		link := strings.TrimRight(raw, ".,;:!?)]}")
		trailing := raw[len(link):]
		fmt.Fprintf(&out, `<a href="%s" style="color:#9f152f;text-decoration:underline">%s</a>`, html.EscapeString(link), html.EscapeString(link))
		out.WriteString(html.EscapeString(trailing))
		last = loc[1]
	}
	out.WriteString(html.EscapeString(text[last:]))
	return out.String()
}
