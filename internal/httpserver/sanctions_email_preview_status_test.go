package httpserver

import "testing"

func TestEffectiveCorrespondenceDisplayStatusUsesActualDelivery(t *testing.T) {
	for _, test := range []struct {
		snapshot string
		delivery string
		want     string
	}{
		{snapshot: "queued", delivery: "sent", want: "sent"},
		{snapshot: "queued", delivery: "failed", want: "failed"},
		{snapshot: "queued", delivery: "", want: "queued"},
		{snapshot: "draft", delivery: "", want: "draft"},
	} {
		if got := effectiveCorrespondenceDisplayStatus(test.snapshot, test.delivery); got != test.want {
			t.Fatalf("effectiveCorrespondenceDisplayStatus(%q,%q)=%q want %q", test.snapshot, test.delivery, got, test.want)
		}
	}
}

func TestCorrespondenceStatusBadgeClassMakesSentClear(t *testing.T) {
	for status, want := range map[string]string{
		"sent":       "text-bg-success",
		"delivered":  "text-bg-success",
		"queued":     "text-bg-primary",
		"failed":     "text-bg-danger",
		"bounced":    "text-bg-danger",
		"not saved":  "text-bg-warning",
	} {
		if got := correspondenceStatusBadgeClass(status); got != want {
			t.Fatalf("correspondenceStatusBadgeClass(%q)=%q want %q", status, got, want)
		}
	}
}
