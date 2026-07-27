package httpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"cricket-ground-feedback/internal/portal"
)

const rulesAssistantAssetVersion = "20260724-1"
const brandAssetVersion = "20260725-2"

const (
	bootstrapCSS = "https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css"
	bootstrapJS  = "https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/js/bootstrap.bundle.min.js"
	htmxJS       = "https://unpkg.com/htmx.org@1.9.12"
	chartJS      = "https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js"
)

func rulesAssistantEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RULES_ASSISTANT_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func rulesAssistantStylesheet() string {
	if !rulesAssistantEnabled() {
		return ""
	}
	return `<link href="/static/css/rules-assistant.css?v=` + rulesAssistantAssetVersion + `" rel="stylesheet">`
}

func adminRulesAssistantAssets(csrfToken string) string {
	config, _ := json.Marshal(map[string]any{
		"admin":            true,
		"chatEndpoint":     "/admin/api/rules/chat",
		"feedbackEndpoint": "/admin/api/rules/chat/feedback",
		"fullURL":          "/admin/rules-assistant",
		"csrfToken":        csrfToken,
	})
	return fmt.Sprintf(`<link href="/static/css/rules-assistant.css?v=%s" rel="stylesheet">
<script>window.GMCLRulesAssistantConfig=%s;</script>
<script src="/static/rules-assistant.js?v=%s" defer></script>`, rulesAssistantAssetVersion, config, rulesAssistantAssetVersion)
}

// pageHead writes the opening HTML through <body> with Bootstrap CSS, brand CSS, and HTMX.
func pageHead(w io.Writer, title string) {
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="theme-color" content="#C41E3A">
  <meta name="apple-mobile-web-app-capable" content="yes">
  <meta name="apple-mobile-web-app-title" content="GMCL">
  <meta name="apple-mobile-web-app-status-bar-style" content="default">
  <title>%s — GMCL</title>
  <link rel="manifest" href="/manifest.webmanifest">
  <link rel="apple-touch-icon" href="/static/icons/apple-touch-icon.png">
  <link href="%s" rel="stylesheet">
  <link href="/static/css/brand.css?v=%s" rel="stylesheet">
  %s
  <script src="%s"></script>
</head>
<body>
`, escapeHTML(title), bootstrapCSS, brandAssetVersion, rulesAssistantStylesheet(), htmxJS)
}

// pageHeadWithCharts writes the opening HTML including Chart.js for chart-heavy pages.
func pageHeadWithCharts(w io.Writer, title string) {
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="theme-color" content="#C41E3A">
  <meta name="apple-mobile-web-app-capable" content="yes">
  <meta name="apple-mobile-web-app-title" content="GMCL Admin">
  <meta name="apple-mobile-web-app-status-bar-style" content="default">
  <title>%s — GMCL Admin</title>
  <link rel="manifest" href="/manifest.webmanifest">
  <link rel="apple-touch-icon" href="/static/icons/apple-touch-icon.png">
  <link href="%s" rel="stylesheet">
  <link href="/static/css/brand.css?v=%s" rel="stylesheet">
  %s
  <script src="%s"></script>
  <script src="%s"></script>
</head>
<body>
`, escapeHTML(title), bootstrapCSS, brandAssetVersion, rulesAssistantStylesheet(), htmxJS, chartJS)
}

// writeCaptainNav writes a simple top navbar with logo and app name.
func writeCaptainNav(w io.Writer) {
	assistantLink := ""
	if rulesAssistantEnabled() {
		assistantLink = `<li class="nav-item"><a class="nav-link" href="/rules-assistant">Hawk AI</a></li>`
	}
	fmt.Fprintf(w, `<nav class="navbar navbar-expand-md navbar-dark bg-gmcl mb-4">
  <div class="container">
    <a class="navbar-brand d-flex align-items-center" href="/">
      <img src="/images/logo.webp" alt="GMCL" height="48" class="me-2">
    </a>
    <button class="navbar-toggler" type="button" data-bs-toggle="collapse" data-bs-target="#publicNav" aria-controls="publicNav" aria-expanded="false" aria-label="Toggle navigation">
      <span class="navbar-toggler-icon"></span>
    </button>
    <div class="collapse navbar-collapse" id="publicNav">
      <ul class="navbar-nav ms-auto mb-2 mb-md-0">
        <li class="nav-item"><a class="nav-link" href="/">Home</a></li>
        <li class="nav-item"><a class="nav-link" href="https://sanctions.gmcl.co.uk/">Sanctions register</a></li>
        <li class="nav-item"><a class="nav-link" href="/submissions">Submission status</a></li>
        %s
        <li class="nav-item"><a class="nav-link" href="/privacy">Privacy</a></li>
        <li class="nav-item"><a class="nav-link" href="/retention">Retention</a></li>
      </ul>
    </div>
  </div>
</nav>
`, assistantLink)
}

// writeAdminNav writes the admin navbar with dropdowns, active-link highlighting, and logout.
func writeAdminNav(w io.Writer, csrfToken, activePath string, roleOpt ...string) {
	fmt.Fprint(w, adminRulesAssistantAssets(csrfToken))
	role := "admin"
	if len(roleOpt) > 0 && roleOpt[0] != "" {
		role = roleOpt[0]
	}
	navLink := func(href, label string) string {
		active := ""
		if strings.HasPrefix(activePath, href) {
			active = " active"
		}
		return fmt.Sprintf(`<li class="nav-item"><a class="nav-link%s" href="%s">%s</a></li>`,
			active, href, label)
	}

	dropdownActive := func(prefixes ...string) string {
		for _, p := range prefixes {
			if strings.HasPrefix(activePath, p) {
				return " active"
			}
		}
		return ""
	}

	missingReportItem := ""
	starredReplacementItem := ""
	pitchMarksItem := ""
	if role == "super_admin" {
		missingReportItem = `<li><a class="dropdown-item" href="/admin/reports/missing-submissions">Missing Submissions &amp; Cards</a></li>`
		starredReplacementItem = `<li><a class="dropdown-item" href="/admin/starred-player-replacements">Player replacements</a></li>`
		pitchMarksItem = `<li><a class="dropdown-item" href="/admin/pitch-marks">Pitch data import</a></li>`
	}

	sanctionsMenu := fmt.Sprintf(`
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            Sanctions
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/cases">Case dashboard</a></li>
            <li><a class="dropdown-item" href="/admin/cases/new">Add card, ban, fine or points decision</a></li>
            <li><a class="dropdown-item" href="/admin/cases/imports">Import legacy bans &amp; cards</a></li>
            <li><a class="dropdown-item" href="/admin/cases/tasks">Follow-up tasks</a></li>
            <li><a class="dropdown-item" href="/admin/cases/recipients">Notice recipients</a></li>
            <li><a class="dropdown-item" href="/admin/cases/automation">Automation safety</a></li>
            <li><hr class="dropdown-divider"></li>
            <li><a class="dropdown-item" href="https://sanctions.gmcl.co.uk/" target="_blank" rel="noopener">View public register</a></li>
            <li><a class="dropdown-item" href="/admin/sanctions">Legacy card ledger</a></li>
          </ul>
        </li>`, dropdownActive("/admin/cases", "/admin/sanctions"))

	operationsMenu := fmt.Sprintf(`
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            Match Operations
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/fixtures">Fixtures</a></li>
            <li><a class="dropdown-item" href="/admin/compliance">Weekly compliance</a></li>
            <li><a class="dropdown-item" href="/admin/submissions">Find submissions</a></li>
            <li><a class="dropdown-item" href="/admin/weeks">All weeks</a></li>
            <li><a class="dropdown-item" href="/admin/reminders/preview">Reminder centre</a></li>
            <li><hr class="dropdown-divider"></li>
            <li><a class="dropdown-item" href="/admin/teams-captains">Teams &amp; captains</a></li>
            <li><a class="dropdown-item" href="/admin/captain-preview">Captain form preview</a></li>
          </ul>
        </li>`,
		dropdownActive("/admin/fixtures", "/admin/compliance", "/admin/submissions", "/admin/weeks", "/admin/reminders", "/admin/teams-captains", "/admin/captain-preview"),
	)

	performanceMenu := fmt.Sprintf(`
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            Performance
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/rankings">Club rankings</a></li>
            <li><a class="dropdown-item" href="/admin/rankings/umpires">Umpire rankings</a></li>
            %s
          </ul>
        </li>`,
		dropdownActive("/admin/rankings", "/admin/pitch-marks"),
		pitchMarksItem,
	)

	reportsMenu := fmt.Sprintf(`
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            Reports
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/reports/exec">Executive report</a></li>
            %s
            <li><a class="dropdown-item" href="/admin/reports">Generated reports</a></li>
          </ul>
        </li>`,
		dropdownActive("/admin/reports"),
		missingReportItem,
	)

	portalMessagesItem := ""
	if portal.EnabledFromEnv() {
		portalMessagesItem = navLink("/admin/portal/cases", "Club messages")
	}
	opsMenu := operationsMenu + performanceMenu + sanctionsMenu + reportsMenu +
		navLink("/admin/rules-assistant", "Hawk AI") + portalMessagesItem

	competitionMenu := fmt.Sprintf(`
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            Competition
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/play-cricket">Play-Cricket sync</a></li>
            <li><a class="dropdown-item" href="/admin/starred-players">Starred players</a></li>
            %s
            <li><hr class="dropdown-divider"></li>
            <li><a class="dropdown-item" href="/admin/submissions/import">Legacy submission import</a></li>
            <li><a class="dropdown-item" href="/admin/csv/captains">Captain data import</a></li>
          </ul>
        </li>`,
		dropdownActive("/admin/play-cricket", "/admin/starred-players", "/admin/starred-player-replacements", "/admin/submissions/import", "/admin/csv"),
		starredReplacementItem,
	)

	portalPilotItem := ""
	if portal.EnabledFromEnv() {
		portalPilotItem = `<li><a class="dropdown-item" href="/admin/portal">Club portal pilot</a></li>`
	}
	systemMenu := fmt.Sprintf(`
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            System
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/email-health">Email health</a></li>
            <li><a class="dropdown-item" href="/admin/link-diagnostics">Link diagnostics</a></li>
            <li><a class="dropdown-item" href="/admin/security">Security &amp; privacy</a></li>
            <li><a class="dropdown-item" href="/admin/gdpr">GDPR</a></li>
            <li><a class="dropdown-item" href="/admin/form-settings">Form settings</a></li>
            <li><a class="dropdown-item" href="/admin/users">Users &amp; access</a></li>
            %s
          </ul>
        </li>`,
		dropdownActive("/admin/email-health", "/admin/link-diagnostics", "/admin/security", "/admin/gdpr", "/admin/form-settings", "/admin/users", "/admin/portal"),
		portalPilotItem,
	)

	menu := navLink("/admin/dashboard", "Dashboard") + opsMenu
	if role == "super_admin" {
		menu += competitionMenu + systemMenu
	}
	accountMenu := fmt.Sprintf(`
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            Account
          </a>
          <ul class="dropdown-menu dropdown-menu-dark dropdown-menu-end">
            <li><a class="dropdown-item" href="/admin/change-password">Change Password</a></li>
          </ul>
        </li>`, dropdownActive("/admin/change-password"))
	menu += accountMenu

	fmt.Fprintf(w, `<nav class="navbar navbar-expand-md navbar-dark bg-gmcl mb-0 shadow-sm">
  <div class="container-fluid px-3">
    <a class="navbar-brand d-flex align-items-center" href="/admin/dashboard">
      <img src="/images/logo.webp" alt="GMCL" height="40" class="me-2">
      <span class="fw-semibold fs-6 d-none d-lg-inline">Admin</span>
    </a>
    <button class="navbar-toggler" type="button" data-bs-toggle="collapse" data-bs-target="#adminNav"
            aria-controls="adminNav" aria-expanded="false" aria-label="Toggle navigation">
      <span class="navbar-toggler-icon"></span>
    </button>
    <div class="collapse navbar-collapse" id="adminNav">
      <ul class="navbar-nav me-auto mb-2 mb-md-0">
        %s
      </ul>
      <form method="POST" action="/admin/logout" class="d-flex">
        <input type="hidden" name="csrf_token" value="%s">
        <button class="btn btn-outline-light btn-sm" type="submit">Logout</button>
      </form>
    </div>
  </div>
</nav>
<div class="mb-4"></div>
`,
		menu,
		csrfToken,
	)
}

// pageFooter writes the Bootstrap JS bundle and closing HTML tags.
func pageFooter(w io.Writer) {
	assistantScript := ""
	if rulesAssistantEnabled() {
		assistantScript = `<script src="/static/rules-assistant.js?v=` + rulesAssistantAssetVersion + `" defer></script>`
	}
	fmt.Fprintf(w, `<script src="%s"></script>
%s
<script>
if ("serviceWorker" in navigator) {
  window.addEventListener("load", function () {
    navigator.serviceWorker.register("/service-worker.js").catch(function () {});
  });
}
</script>
</body>
</html>
`, bootstrapJS, assistantScript)
}

// pageFooterWithScript writes Bootstrap JS, then any inline chart/script code, then closes the page.
func pageFooterWithScript(w io.Writer, script string) {
	fmt.Fprintf(w, `<script src="%s"></script>
<script>
if ("serviceWorker" in navigator) {
  window.addEventListener("load", function () {
    navigator.serviceWorker.register("/service-worker.js").catch(function () {});
  });
}
</script>
<script>
%s
</script>
</body>
</html>
`, bootstrapJS, script)
}
