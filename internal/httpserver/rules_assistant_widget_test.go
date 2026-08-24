package httpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rulesAssistantAsset(t *testing.T, name ...string) string {
	t.Helper()
	parts := append([]string{"..", "..", "static"}, name...)
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRulesWidgetCanBeMovedOffPageControls(t *testing.T) {
	script := rulesAssistantAsset(t, "rules-assistant.js")
	for _, want := range []string{
		"pointerdown",
		"pointermove",
		"pointercancel",
		"setPointerCapture",
		"gmcl.hawk-ai.position",
		"localStorage.setItem(positionKey",
		"localStorage.getItem(positionKey",
		"suppressLauncherClick",
		"has-rules-widget",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("the widget cannot be dragged clear of page controls: %q is missing", want)
		}
	}
	if !strings.Contains(script, "window.innerWidth - size.width - edgeMargin") ||
		!strings.Contains(script, "window.innerHeight - size.height - edgeMargin") {
		t.Fatal("a dragged widget must stay inside the viewport")
	}
	if !strings.Contains(script, "resetPosition") || !strings.Contains(script, "event.key === '0'") {
		t.Fatal("there must be a way to put the widget back in its corner")
	}
	if !strings.Contains(script, "ArrowLeft") || !strings.Contains(script, "event.altKey") {
		t.Fatal("the widget must also be movable from the keyboard")
	}
}

func TestRulesWidgetStyleLeavesRoomAndKeepsMobilePanel(t *testing.T) {
	css := rulesAssistantAsset(t, "css", "rules-assistant.css")
	if !strings.Contains(css, "body.has-rules-widget{padding-bottom:") {
		t.Fatal("pages must leave room under the widget's resting corner")
	}
	if !strings.Contains(css, ".rules-widget.is-placed{right:auto;bottom:auto}") {
		t.Fatal("a moved widget must stop being pinned to its corner")
	}

	// The panel is a full-screen sheet under 601px. The flip rules set top/left
	// on that same element, so they must not reach the mobile layout.
	flip := strings.Index(css, ".rules-widget.is-panel-top .rules-widget-panel")
	desktopOnly := strings.Index(css, "@media(min-width:601px)")
	mobile := strings.Index(css, "@media(max-width:600px){.rules-widget{right:12px")
	if flip < 0 || desktopOnly < 0 || mobile < 0 {
		t.Fatalf("expected both panel-flip and mobile rules: flip=%d desktopOnly=%d mobile=%d", flip, desktopOnly, mobile)
	}
	if flip < desktopOnly {
		t.Fatal("panel-flip rules must sit inside the desktop-only media query, or they break the mobile sheet")
	}
}

func TestRulesAssistantAssetVersionCoversTheWidgetChange(t *testing.T) {
	// Both files are served with this cache key, so an unchanged version would
	// leave browsers on the old, unmovable widget.
	if rulesAssistantAssetVersion == "20260728-1" {
		t.Fatal("rulesAssistantAssetVersion was not bumped after changing the widget assets")
	}
	assets := adminRulesAssistantAssets("csrf")
	for _, want := range []string{
		"/static/css/rules-assistant.css?v=" + rulesAssistantAssetVersion,
		"/static/rules-assistant.js?v=" + rulesAssistantAssetVersion,
	} {
		if !strings.Contains(assets, want) {
			t.Fatalf("admin pages do not request %q", want)
		}
	}
}
