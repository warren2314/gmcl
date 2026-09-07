package httpserver

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseClosureClubIDs(t *testing.T) {
	ids, err := parseClosureClubIDs([]string{"12", "34", "12"})
	if err != nil || !reflect.DeepEqual(ids, []int32{12, 34}) {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	for _, values := range [][]string{{"0"}, {"-2"}, {"2147483648"}, {"home"}, make([]string, 21)} {
		if _, err := parseClosureClubIDs(values); err == nil {
			t.Fatalf("accepted %v", values)
		}
	}
	ids, err = parseClosureClubIDs(nil)
	if err != nil || len(ids) != 0 {
		t.Fatal("closing without a notice must remain supported")
	}
}

func TestClosureMailbox(t *testing.T) {
	for _, raw := range []string{"", "bad", "Club <club@example.org>", "club@example.org\r\nBcc: other@example.org"} {
		if _, err := closureMailbox(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if got, err := closureMailbox("  Club@Example.org  "); err != nil || got != "club@example.org" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestClosureNotificationControlsWithinCloseForm(t *testing.T) {
	owner := int32(7)
	html := adminCloseCaseNoActionHTML(42, "csrf", "investigating", false, &owner, &owner, `<input name="notify_club" value="12">`)
	if !strings.Contains(html, `<input name="notify_club" value="12">`) || strings.Index(html, `name="notify_club"`) > strings.Index(html, "</form>") {
		t.Fatal("notification selection missing from closure form")
	}
	if !strings.Contains(html, "Private reason") {
		t.Fatal("must distinguish internal reason from notification")
	}
}

func TestClosureRecipientsSelectEitherBothAndRejectUnrelatedClubs(t *testing.T) {
	clubs := []closureClub{{id: 12, side: "Home", email: "home@example.org"}, {id: 34, side: "Away", email: "away@example.org"}}
	for _, test := range []struct {
		ids  []int32
		want []string
	}{
		{[]int32{12}, []string{"home@example.org"}},
		{[]int32{34}, []string{"away@example.org"}},
		{[]int32{12, 34}, []string{"home@example.org", "away@example.org"}},
	} {
		got, err := closureRecipients(clubs, test.ids)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("recipients=%v err=%v want=%v", got, err, test.want)
		}
	}
	if _, err := closureRecipients(clubs, []int32{12, 99}); err == nil {
		t.Fatal("accepted unrelated club")
	}
	clubs[1].email = "HOME@example.org"
	if got, err := closureRecipients(clubs, []int32{12, 34}); err != nil || len(got) != 1 {
		t.Fatalf("shared mailbox not deduplicated: %v %v", got, err)
	}
	clubs[1].email = ""
	if _, err := closureRecipients(clubs, []int32{12, 34}); err == nil {
		t.Fatal("accepted missing mailbox")
	}
}
