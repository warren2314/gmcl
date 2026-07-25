package httpserver

import (
	"bytes"
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
	for _, want := range []string{"Sanctions", "Add card, ban, fine or points decision", "Import legacy bans &amp; cards", "Follow-up tasks", "View public register", "/admin/api/rules/chat", "GMCLRulesAssistantConfig", "/static/rules-assistant.js"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin navigation missing %q", want)
		}
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
