package httpserver

import (
	"reflect"
	"testing"
)

func TestParseSanctionImportCSVNormalisesHeadersWithoutChangingRows(t *testing.T) {
	headers, rows, err := parseSanctionImportCSV([]byte("Club,,Club\nAlpha,value,duplicate\n"))
	if err != nil {
		t.Fatalf("parseSanctionImportCSV: %v", err)
	}
	wantHeaders := []string{"Club", "column_2", "Club_2"}
	if !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("headers = %#v, want %#v", headers, wantHeaders)
	}
	wantRows := [][]string{{"Alpha", "value", "duplicate"}}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("rows = %#v, want %#v", rows, wantRows)
	}
}

func TestParseSanctionImportCSVRejectsEmptyInput(t *testing.T) {
	if _, _, err := parseSanctionImportCSV(nil); err == nil {
		t.Fatal("expected empty CSV to be rejected")
	}
}

func TestLiveSanctionFeedsAreUniquePublishedCSVFeeds(t *testing.T) {
	if len(liveSanctionFeeds) != 3 {
		t.Fatalf("feed count = %d, want 3 unique datasets", len(liveSanctionFeeds))
	}
	seen := map[string]bool{}
	for _, feed := range liveSanctionFeeds {
		if seen[feed.URL] {
			t.Fatalf("duplicate feed URL: %s", feed.URL)
		}
		seen[feed.URL] = true
		if feed.Name == "" || feed.Filename == "" {
			t.Fatalf("incomplete feed definition: %#v", feed)
		}
	}
}
