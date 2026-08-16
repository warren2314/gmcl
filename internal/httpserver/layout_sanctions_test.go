package httpserver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicNavigationExposesSanctionsAndCollapsesOnMobile(t *testing.T) {
	var out bytes.Buffer
	writeCaptainNav(&out)
	html := out.String()
	for _, want := range []string{`href="https://sanctions.gmcl.co.uk/"`, `Sanctions register`, `navbar-toggler`, `id="publicNav"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("public navigation missing %q", want)
		}
	}
}

func TestAdminNavigationExposesSanctionsWorkflow(t *testing.T) {
	var out bytes.Buffer
	writeAdminNav(&out, "csrf", "/admin/cases/imports", "super_admin")
	html := out.String()
	for _, want := range []string{"admin-navbar", "Sanctions", "My ineligible-player cases", "/admin/ineligible?scope=mine&amp;state=all&amp;worklist=visible", "Add card, ban, fine or points decision", "Import legacy bans &amp; cards", "Follow-up tasks", "View public register", "/admin/api/rules/chat", "GMCLRulesAssistantConfig", "/static/rules-assistant.js"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin navigation missing %q", want)
		}
	}
}

func TestAdminNavigationLayersAboveStickyPageNavigation(t *testing.T) {
	brandCSS, err := os.ReadFile(filepath.Join("..", "..", "static", "css", "brand.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(brandCSS)
	if !strings.Contains(css, ".admin-navbar {") || !strings.Contains(css, "z-index: 1030;") {
		t.Fatal("admin navigation must stay above Bootstrap sticky page navigation at z-index 1020")
	}
}

func TestAdminNavigationGroupsWorkByUserWorkflow(t *testing.T) {
	var out bytes.Buffer
	writeAdminNav(&out, "csrf", "/admin/compliance", "super_admin")
	html := out.String()

	for _, want := range []string{
		"Match Operations",
		"Performance",
		"Sanctions",
		"Reports",
		"Competition",
		"System",
		"Weekly compliance",
		"Teams &amp; captains",
		"Users &amp; access",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("admin navigation missing workflow label %q", want)
		}
	}
}

func TestStandardAdminNavigationOmitsSuperAdminTools(t *testing.T) {
	var out bytes.Buffer
	writeAdminNav(&out, "csrf", "/admin/dashboard", "admin")
	html := out.String()

	for _, forbidden := range []string{
		"Competition",
		"System",
		"/admin/pitch-marks",
		"/admin/reports/missing-submissions",
	} {
		if strings.Contains(html, forbidden) {
			t.Errorf("standard admin navigation exposes super-admin item %q", forbidden)
		}
	}
}
