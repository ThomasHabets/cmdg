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

func TestScreenIgnoresOffscreenWrites(t *testing.T) {
	s := NewScreen2(10, 2)

	s.Printlnf(-1, "bad")
	s.Printlnf(2, "bad")
	s.Printf(-1, 0, "bad")
	s.Printf(2, 0, "bad")

	for n, line := range s.buffer {
		if line != "" {
			t.Fatalf("line %d was changed to %q", n, line)
		}
	}
}

func TestScreenClampsNegativePrintfColumn(t *testing.T) {
	s := NewScreen2(10, 1)
	s.Printf(0, -5, "ok")

	if got, want := s.buffer[0], "ok"; got != want {
		t.Fatalf("Printf at negative column got %q, want %q", got, want)
	}
}

func TestSetCursorRejectsOffscreenRow(t *testing.T) {
	s := NewScreen2(10, 1)
	s.SetCursor(-1, 0)
	if s.cursor != nil {
		t.Fatalf("SetCursor with negative row set cursor to %+v", s.cursor)
	}
	s.SetCursor(1, 0)
	if s.cursor != nil {
		t.Fatalf("SetCursor with offscreen row set cursor to %+v", s.cursor)
	}
}

func TestSetCursorClampsNegativeColumn(t *testing.T) {
	s := NewScreen2(10, 1)
	s.SetCursor(0, -5)
	if s.cursor == nil {
		t.Fatal("SetCursor did not set cursor")
	}
	if got, want := s.cursor.x, 0; got != want {
		t.Fatalf("cursor x got %d, want %d", got, want)
	}
}
