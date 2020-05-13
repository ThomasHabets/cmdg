package display

import (
	"testing"
)

func TestStripANSI(t *testing.T) {
	for _, test := range []struct {
		in  string
		out string
	}{
		{"", ""},
		{"hello", "hello"},
		{"\x1Bhello", "hello"},
		{"\x1Bhell\x1Bo på dig", "hello på dig"},
	} {
		if got, want := stripANSI(test.in), test.out; got != want {
			t.Errorf("For %q: got %q, want %q", test.in, got, want)
		}
	}
}

func TestStringWidth(t *testing.T) {
	for _, test := range []struct {
		in  string
		out int
	}{
		{"", 0},
		{"hello", 5},
		{"\x1Bhello", 5},
		{"\x1Bhell\x1Bo", 5},
		{"för räksmörgås", 14},
		{"ಠ_ಠ", 3},
	} {
		if got, want := StringWidth(test.in), test.out; got != want {
			t.Errorf("For %q: got %d, want %d", test.in, got, want)
		}
	}
}
func TestFixedWidth(t *testing.T) {
	for _, test := range []struct {
		in  string
		w   int
		out string
	}{
		{"", 0, ""},
		{"", 2, "  "},
		{"hello", 5, "hello"},
		{"hello", 2, "he"},
		{"hello", 10, "     hello"},
		{"\x1Bhello", 5, "\x1Bhello"},
		{"för räksmörgås", 14, "för räksmörgås"},
		{"för \x1Bräksmörgås", 15, " för \x1Bräksmörgås"},
		{"ಠ_ಠ", 3, "ಠ_ಠ"},
	} {
		if got, want := FixedWidth(test.in, test.w), test.out; got != want {
			t.Errorf("For %q: got %q, want %q", test.in, got, want)
		}
	}
}
