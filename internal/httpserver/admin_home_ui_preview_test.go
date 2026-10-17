package httpserver

import (
	"encoding/base64"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestHomeWorkspacePagePreview uses production renderers and fictional records.
// It writes no files unless GMCL_UI_PREVIEW_DIR is explicitly supplied. Local
// Bootstrap assets can be supplied with GMCL_UI_ASSET_DIR for offline visual QA.
// The fixture does not start an application, access a database or send mail.
func TestHomeWorkspacePagePreview(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, loc)
	due, future := now.Add(-time.Hour), time.Date(2027, 4, 17, 0, 0, 0, 0, loc)
	data := personalWorkDashboard{
		AdminName: "Dave · fictional UI fixture", CanInvestigate: true, CanApprove: true, CanTriage: true,
		AssignedTotal: 3,
		AssignedCases: []personalWorkCase{
			{ID: 271, Reference: "IP-271", Status: "investigating", Player: "Example Player A", Club: "Northfield Example CC", Team: "2nd XI", UpdatedAt: now},
			{ID: 272, Reference: "IP-272", Status: "response_pending", Player: "Example Player B", Club: "Brookside Example CC", Team: "1st XI", UpdatedAt: now},
			{ID: 274, Reference: "IP-274", Status: "approved", Player: "Example Player D", Club: "Meadow Example CC", Team: "1st XI", UpdatedAt: now},
		},
		ResponseTotal: 1, Responses: []personalWorkResponse{{CaseID: 271, Reference: "IP-271", Player: "Example Player A", Club: "Northfield Example CC", ReceivedAt: now.Add(-18 * time.Hour)}},
		DecisionTotal: 1, DecisionQueue: []personalWorkQueueItem{{CaseID: 273, Reference: "IP-273", Action: "Review decision", Player: "Example Player C", Club: "Hilltop Example CC"}},
		TaskTotal: 2, Tasks: []personalWorkTask{
			{ID: 40, CaseID: 260, Reference: "IP-260 · completed case", Note: "Check delivery of the issued outcome", DueAt: &due},
			{ID: 41, CaseID: 274, Reference: "IP-274", Note: "Apply the approved future-season points instruction", NotBefore: &future},
		},
	}
	counts := ineligibleDashboardCounts{LiveCases: 5, AwaitingDenverSignoff: 1, DeliveryExceptions: 1, NewIntakes: 2, HiddenReports: 1}
	output := httptest.NewRecorder()
	pageHead(output, "Home · local UI verification")
	writeAdminNavWithPermissions(output, "fictional-preview-token", "/admin/dashboard", "admin", map[string]bool{
		"sanctions_triage": true, "sanctions_investigate": true, "sanctions_approve": true,
	})
	output.WriteString(`<main class="container-fluid px-4 admin-workspace"><div class="my-4"><h1 class="h3">Home</h1><p class="text-muted">Fictional UI verification · production Home action and league-overview renderers · no live connection</p></div>`)
	writePersonalWorkDashboard(output, data, loc, now)
	writeAdminHomeLeagueOverview(output, counts, nil, true)
	output.WriteString(`</main>`)
	pageFooter(output)
	html := offlineHomePreviewHTML(t, output.Body.String())
	for _, want := range []string{"<!doctype html>", "<nav", "My work", "League overview", "2 reports awaiting triage", "Available from 17 Apr 2027", "completed case", "return_to=%2Fadmin%2Fdashboard", "</body>"} {
		if !strings.Contains(html, want) {
			t.Errorf("production fixture missing %q", want)
		}
	}
	if strings.Contains(html, `<script src="https://`) || strings.Contains(html, `<link href="https://`) || strings.Contains(html, "navigator.serviceWorker") {
		t.Fatal("offline fixture loads network assets or registers a service worker")
	}
	if directory := strings.TrimSpace(os.Getenv("GMCL_UI_PREVIEW_DIR")); directory != "" {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "home-workspace.html")
		if err := os.WriteFile(path, []byte(html), 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("Offline Home fixture: %s", path)
	}
}

func offlineHomePreviewHTML(t *testing.T, html string) string {
	t.Helper()
	// Keep the exact page wrappers and navigation markup, replacing resources
	// only in the test output. No preview-specific production endpoint exists.
	html = regexp.MustCompile(`(?s)<script\b[^>]*>.*?</script>`).ReplaceAllString(html, "")
	html = regexp.MustCompile(`<link rel="(?:manifest|apple-touch-icon)"[^>]*>`).ReplaceAllString(html, "")
	styles := regexp.MustCompile(`<link href="([^"]+)" rel="stylesheet">`)
	html = styles.ReplaceAllStringFunc(html, func(link string) string {
		var path string
		switch {
		case strings.Contains(link, bootstrapCSS):
			if directory := strings.TrimSpace(os.Getenv("GMCL_UI_ASSET_DIR")); directory != "" {
				path = filepath.Join(directory, "bootstrap-5.3.3.min.css")
			}
		case strings.Contains(link, "/static/css/brand.css"):
			path = filepath.Join("..", "..", "static", "css", "brand.css")
		case strings.Contains(link, "/static/css/rules-assistant.css"):
			path = filepath.Join("..", "..", "static", "css", "rules-assistant.css")
		}
		if path == "" {
			return `<!-- Bootstrap CSS not supplied: structural fixture only; set GMCL_UI_ASSET_DIR for visual QA. -->`
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read local preview stylesheet %s: %v", path, err)
		}
		return "<style>" + string(data) + "</style>"
	})
	if logo, err := os.ReadFile(filepath.Join("..", "..", "images", "logo.webp")); err == nil {
		html = strings.ReplaceAll(html, `src="/images/logo.webp"`, `src="data:image/webp;base64,`+base64.StdEncoding.EncodeToString(logo)+`"`)
	} else {
		t.Fatal(err)
	}
	if directory := strings.TrimSpace(os.Getenv("GMCL_UI_ASSET_DIR")); directory != "" {
		if js, err := os.ReadFile(filepath.Join(directory, "bootstrap.bundle.min.js")); err == nil {
			html = strings.Replace(html, "</body>", "<script>"+string(js)+"</script></body>", 1)
		}
	}
	return html
}
