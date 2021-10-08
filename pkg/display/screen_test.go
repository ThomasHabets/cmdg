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
		{"ಠ_ಠ", 4, " ಠ_ಠ"},
		{"ಠ_ಠ", 2, "ಠ_"},
	} {
		if got, want := FixedWidth(test.in, test.w), test.out; got != want {
			t.Errorf("For %q: got %q, want %q", test.in, got, want)
		}
	}
}

func TestFixedANSIWidthRight(t *testing.T) {
	for _, test := range []struct {
		in  string
		w   int
		out string
	}{
		{"", 0, ""},
		{"", 2, "  "},
		{"hello", 5, "hello"},
		{"hello", 2, "he"},
		{"hello", 10, "hello     "},

		// Test ANSI length taken into account.
		{"\x1B[2mhello", 5, "\x1B[2mhello"},

		// ANSI being cut off.
		{"\x1B[2mhello world\x1B[2m", 5, "\x1B[2mhello"},

		// ANSI at the end of *not* cutoff
		{"\x1B[2mhello\x1B[2m world", 5, "\x1B[2mhello\x1B[2m"},

		// ANSI cutoff, and cut off some non-ansi too.
		{"\x1B[2mhello", 3, "\x1B[2mhel"},

		// Unicode.
		{"för räksmörgås", 14, "för räksmörgås"},
		{"för \x1B[1mräksmörgås", 15, "för \x1B[1mräksmörgås "},
		{"ಠ_ಠ", 3, "ಠ_ಠ"},
		{"ಠ_ಠ", 4, "ಠ_ಠ "},
		{"ಠ_ಠ", 2, "ಠ_"},
	} {
		if got, want := FixedANSIWidthRight(test.in, test.w), test.out; got != want {
			t.Errorf("For %q: got:\n  %q\nwant:\n  %q", test.in, got, want)
		}
	}
}
