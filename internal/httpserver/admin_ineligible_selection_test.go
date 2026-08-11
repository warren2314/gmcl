package httpserver

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	ineligibledomain "cricket-ground-feedback/internal/ineligible"
)

func TestParseIneligibleSelectionIDs(t *testing.T) {
	t.Run("preserves valid selection order", func(t *testing.T) {
		got, err := parseIneligibleSelectionIDs([]string{"42", " 7 ", "19"})
		if err != nil {
			t.Fatalf("parse valid selection: %v", err)
		}
		want := []int64{42, 7, 19}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("selection IDs = %#v, want %#v", got, want)
		}
	})

	invalid := map[string][]string{
		"missing":          nil,
		"blank":            {" "},
		"zero":             {"0"},
		"negative":         {"-1"},
		"not a number":     {"report-7"},
		"integer overflow": {"9223372036854775808"},
		"duplicate":        {"7", " 7 "},
	}
	for name, values := range invalid {
		t.Run("rejects "+name, func(t *testing.T) {
			got, err := parseIneligibleSelectionIDs(values)
			if !errors.Is(err, ineligibledomain.ErrWorklistSelectionInvalid) {
				t.Fatalf("error = %v, want ErrWorklistSelectionInvalid", err)
			}
			if got != nil {
				t.Fatalf("invalid selection returned IDs: %#v", got)
			}
		})
	}

	t.Run("rejects selections over the candidate limit", func(t *testing.T) {
		values := make([]string, ineligibledomain.MaxWorklistCandidates+1)
		for i := range values {
			values[i] = strconv.Itoa(i + 1)
		}
		got, err := parseIneligibleSelectionIDs(values)
		if !errors.Is(err, ineligibledomain.ErrWorklistSelectionInvalid) {
			t.Fatalf("error = %v, want ErrWorklistSelectionInvalid", err)
		}
		if got != nil {
			t.Fatalf("oversized selection returned IDs: %#v", got)
		}
	})
}

func TestParseIneligibleQueueFiltersNormalisesWorklist(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "missing defaults to selected", want: "visible"},
		{name: "selected", value: "visible", want: "visible"},
		{name: "hidden", value: " deferred ", want: "deferred"},
		{name: "all imported", value: "all", want: "all"},
		{name: "unknown defaults to selected", value: "deleted", want: "visible"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := parseIneligibleQueueFilters(url.Values{"worklist": {test.value}})
			if got.Worklist != test.want {
				t.Fatalf("worklist = %q, want %q", got.Worklist, test.want)
			}
		})
	}
}

func TestBuildIneligibleQueueQueryWorklistPredicates(t *testing.T) {
	const visiblePredicate = "(COALESCE(worklist.visibility,'visible')='visible' OR c.id IS NOT NULL)"
	const deferredPredicate = "COALESCE(worklist.visibility,'visible')='deferred' AND c.id IS NULL"
	const worklistJoin = "LEFT JOIN sanction_intake_worklist_current worklist ON worklist.intake_id=i.id"

	for _, test := range []struct {
		name          string
		worklist      string
		wantPredicate string
		absent        string
	}{
		{name: "missing uses selected reports", wantPredicate: visiblePredicate, absent: deferredPredicate},
		{name: "selected reports include linked cases", worklist: "visible", wantPredicate: visiblePredicate, absent: deferredPredicate},
		{name: "hidden reports exclude linked cases", worklist: "deferred", wantPredicate: deferredPredicate, absent: visiblePredicate},
		{name: "all imported reports", worklist: "all", absent: visiblePredicate},
	} {
		t.Run(test.name, func(t *testing.T) {
			query, args := buildIneligibleQueueQueryForAdmin(ineligibleQueueFilters{
				State:    "all",
				Scope:    "all",
				Worklist: test.worklist,
				Sort:     "newest",
			}, nil)
			if !strings.Contains(query, worklistJoin) {
				t.Fatalf("query does not join current work-list state: %s", query)
			}
			if test.wantPredicate != "" && !strings.Contains(query, test.wantPredicate) {
				t.Fatalf("query missing predicate %q: %s", test.wantPredicate, query)
			}
			if test.absent != "" && strings.Contains(query, test.absent) {
				t.Fatalf("query unexpectedly contains predicate %q: %s", test.absent, query)
			}
			if test.worklist == "all" && strings.Contains(query, deferredPredicate) {
				t.Fatalf("all-imported query unexpectedly filters deferred reports: %s", query)
			}
			if len(args) != 0 {
				t.Fatalf("work-list-only query args = %#v, want none", args)
			}
		})
	}
}

func TestWriteIneligibleFiltersPreservesWorklist(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeIneligibleFilters(recorder, ineligibleQueueFilters{
		State:         "open",
		ReportingClub: `Reporting"><script>alert(1)</script>& CC`,
		Scope:         "mine",
		Worklist:      "deferred",
		Sort:          "newest",
	})
	html := recorder.Body.String()

	for _, want := range []string{
		`name="worklist"`,
		`<option value="deferred" selected>Hidden reports</option>`,
		`Current queue: Hidden reports`,
		`<input type="hidden" name="scope" value="mine">`,
		`href="/admin/ineligible?scope=mine&amp;worklist=visible"`,
		`value="Reporting&quot;&gt;&lt;script&gt;alert(1)&lt;/script&gt;&amp; CC"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("filter HTML missing %q", want)
		}
	}
	if strings.Contains(html, `<script>alert(1)</script>`) {
		t.Fatalf("filter HTML contains unescaped input: %s", html)
	}
}

func TestIneligibleQueueUsesAdvancedViewForNonDefaultWorklists(t *testing.T) {
	filter := parseIneligibleQueueFilters(url.Values{})
	if ineligibleQueueUsesAdvancedView(filter) {
		t.Fatalf("selected-report default unexpectedly opens manager controls: %#v", filter)
	}

	for _, worklist := range []string{"deferred", "all"} {
		filter.Worklist = worklist
		if !ineligibleQueueUsesAdvancedView(filter) {
			t.Errorf("worklist %q did not open manager controls", worklist)
		}
	}
}

func TestWriteIneligibleStartRoutesSelectionCopyAndEscaping(t *testing.T) {
	var out bytes.Buffer
	csrf := `token"><script>alert(1)</script>&`
	writeIneligibleStartRoutes(&out, csrf, 0)
	html := out.String()

	for _, want := range []string{
		`Import and choose reports`,
		`tick the reports you have been asked to progress`,
		`method="POST" action="/admin/ineligible/sync"`,
		`href="/admin/ineligible?scope=all&amp;state=open&amp;worklist=visible#reports"`,
		`value="token&quot;&gt;&lt;script&gt;alert(1)&lt;/script&gt;&amp;"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("start-route HTML missing %q", want)
		}
	}
	if strings.Contains(html, `<script>alert(1)</script>`) {
		t.Fatalf("start-route HTML contains unescaped CSRF value: %s", html)
	}
}

func TestPlainIneligibleWorklistUsesBoardFriendlyLabels(t *testing.T) {
	for value, want := range map[string]string{
		"visible":  "Selected reports",
		"deferred": "Hidden reports",
		"all":      "All imported reports",
		"unknown":  "Selected reports",
	} {
		if got := plainIneligibleWorklist(value); got != want {
			t.Errorf("plainIneligibleWorklist(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestRequestIDUsesMiddlewareGeneratedID(t *testing.T) {
	var got string
	handler := chimiddleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = requestID(r)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.TrimSpace(got) == "" {
		t.Fatal("requestID did not return the middleware-generated browser request ID")
	}
}

func TestIneligibleSelectionPostRedirectsEmptySelection(t *testing.T) {
	form := url.Values{
		"run_id":           {"9"},
		"base_batch_id":    {"0"},
		"candidate_sha256": {"unused-for-empty-selection"},
		"reason":           {"Rev 8 blue rows"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/ineligible/selection", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	(&Server{}).handleAdminIneligibleSelectionPost().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if location.Path != "/admin/ineligible/selection" || location.Query().Get("run_id") != "9" {
		t.Fatalf("redirect = %q, want selection page for run 9", location.String())
	}
	if !strings.Contains(location.Query().Get("error"), "Select at least one") {
		t.Fatalf("redirect error = %q, want friendly selection message", location.Query().Get("error"))
	}
}
