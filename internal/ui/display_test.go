package ui

import "testing"

func TestFirstNonEmptyPreservesDisplayValue(t *testing.T) {
	for _, tt := range []struct {
		values []string
		want   string
	}{
		{nil, ""}, {[]string{"", ""}, ""}, {[]string{"", "current", "fallback"}, "current"},
		{[]string{" ", "fallback"}, " "}, {[]string{"raw <&>\n", "fallback"}, "raw <&>\n"},
	} {
		if got := FirstNonEmpty(tt.values...); got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.values, got, tt.want)
		}
	}
}
