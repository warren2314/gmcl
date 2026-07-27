package portal

import "testing"

func TestNormalizedUserActivityLimit(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{-1, defaultUserActivityLimit},
		{0, defaultUserActivityLimit},
		{1, 1},
		{75, 75},
		{maxUserActivityLimit, maxUserActivityLimit},
		{maxUserActivityLimit + 1, maxUserActivityLimit},
	}
	for _, test := range tests {
		if got := normalizedUserActivityLimit(test.input); got != test.want {
			t.Fatalf("limit %d = %d, want %d", test.input, got, test.want)
		}
	}
}
