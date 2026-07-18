package httpserver

import (
	"fmt"
	"io"
	"strings"
)

const (
	bootstrapCSS = "https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css"
	bootstrapJS  = "https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/js/bootstrap.bundle.min.js"
	htmxJS       = "https://unpkg.com/htmx.org@1.9.12"
	chartJS      = "https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js"
)

// pageHead writes the opening HTML through <body> with Bootstrap CSS, brand CSS, and HTMX.
func pageHead(w io.Writer, title string) {
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
  <link href="/static/css/brand.css" rel="stylesheet">
  <link href="/static/css/rules-assistant.css?v=20260714-6" rel="stylesheet">
  <script src="%s"></script>
</head>
<body>
`, escapeHTML(title), bootstrapCSS, htmxJS)
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
  <link href="/static/css/brand.css" rel="stylesheet">
  <link href="/static/css/rules-assistant.css?v=20260714-6" rel="stylesheet">
  <script src="%s"></script>
  <script src="%s"></script>
</head>
<body>
`, escapeHTML(title), bootstrapCSS, htmxJS, chartJS)
}

// writeCaptainNav writes a simple top navbar with logo and app name.
func writeCaptainNav(w io.Writer) {
	fmt.Fprint(w, `<nav class="navbar navbar-dark bg-gmcl mb-4">
  <div class="container">
    <div class="d-flex align-items-center justify-content-between w-100">
      <a class="navbar-brand d-flex align-items-center" href="/">
        <img src="/images/logo.webp" alt="GMCL" height="48" class="me-2">
      </a>
      <div class="d-flex gap-3">
        <a class="link-light text-decoration-none small" href="/">Home</a>
        <a class="link-light text-decoration-none small" href="/submissions">Submission Status</a>
        <a class="link-light text-decoration-none small" href="/rules-assistant">A1 Rules Assistant</a>
        <a class="link-light text-decoration-none small" href="/privacy">Privacy</a>
        <a class="link-light text-decoration-none small" href="/retention">Retention</a>
      </div>
    </div>
  </div>
</nav>
`)
}

// writeAdminNav writes the admin navbar with dropdowns, active-link highlighting, and logout.
func writeAdminNav(w io.Writer, csrfToken, activePath string, roleOpt ...string) {
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
	if role == "super_admin" {
		missingReportItem = `<li><a class="dropdown-item" href="/admin/reports/missing-submissions">Missing Submissions</a></li>`
	}

	opsMenu := fmt.Sprintf(`
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            Submissions
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/submissions">Search by Club</a></li>
            <li><a class="dropdown-item" href="/admin/weeks">Weeks</a></li>
            <li><a class="dropdown-item" href="/admin/compliance">Compliance</a></li>
            <li><a class="dropdown-item" href="/admin/reminders/preview">Reminder Preview</a></li>
            <li><a class="dropdown-item" href="/admin/captain-preview">Captain Form Preview</a></li>
          </ul>
        </li>
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            Rankings
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/rankings">Club Rankings</a></li>
            <li><a class="dropdown-item" href="/admin/rankings/umpires">Umpire Rankings</a></li>
          </ul>
        </li>
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            Reports
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/reports/exec">Executive Report</a></li>
            %s
            <li><a class="dropdown-item" href="/admin/reports">Generated Reports</a></li>
          </ul>
        </li>
		%s
		%s
		%s
		%s
		%s`,
		dropdownActive("/admin/submissions", "/admin/weeks", "/admin/compliance", "/admin/reminders", "/admin/captain-preview"),
		dropdownActive("/admin/rankings"),
		dropdownActive("/admin/reports"),
		missingReportItem,
		navLink("/admin/cases", "Sanctions cases"),
		navLink("/admin/sanctions", "Legacy cards"),
		navLink("/admin/rules-assistant", "A1 Rules Assistant"),
		navLink("/admin/fixtures", "Fixtures"),
		navLink("/admin/teams-captains", "Teams & Captains"),
	)

	systemMenu := fmt.Sprintf(`
        <li class="nav-item dropdown">
          <a class="nav-link dropdown-toggle%s" href="#" role="button" data-bs-toggle="dropdown">
            System
          </a>
          <ul class="dropdown-menu dropdown-menu-dark">
            <li><a class="dropdown-item" href="/admin/email-health">Email Health</a></li>
            <li><a class="dropdown-item" href="/admin/link-diagnostics">Link Diagnostics</a></li>
            <li><a class="dropdown-item" href="/admin/play-cricket">Play-Cricket</a></li>
			<li><a class="dropdown-item" href="/admin/starred-players">Starred Players</a></li>
            <li><a class="dropdown-item" href="/admin/submissions/import">Import Legacy Submissions</a></li>
            <li><a class="dropdown-item" href="/admin/security">Security & Privacy</a></li>
            <li><a class="dropdown-item" href="/admin/gdpr">GDPR</a></li>
            <li><a class="dropdown-item" href="/admin/form-settings">Form Settings</a></li>
            <li><a class="dropdown-item" href="/admin/users">Admin Users</a></li>
            <li><a class="dropdown-item" href="/admin/csv/captains">Captain CSV Upload</a></li>
          </ul>
        </li>`,
		dropdownActive("/admin/email-health", "/admin/link-diagnostics", "/admin/play-cricket", "/admin/starred-players", "/admin/submissions/import", "/admin/security", "/admin/gdpr", "/admin/form-settings", "/admin/users", "/admin/csv"),
	)

	menu := navLink("/admin/dashboard", "Dashboard") + opsMenu
	if role == "super_admin" {
		menu += systemMenu
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
	fmt.Fprintf(w, `<script src="%s"></script>
<script src="/static/rules-assistant.js?v=20260714-6" defer></script>
<script>
if ("serviceWorker" in navigator) {
  window.addEventListener("load", function () {
    navigator.serviceWorker.register("/service-worker.js").catch(function () {});
  });
}
</script>
</body>
</html>
`, bootstrapJS)
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
