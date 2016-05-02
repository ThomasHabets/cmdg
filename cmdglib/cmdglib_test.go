package cmdglib

import (
	"testing"
)

func TestDateFormats(t *testing.T) {
	for _, test := range []struct {
		input string
		error bool
	}{
		{"Mon, 2 Jan 2006 15:04:05 -0700", false},
		{"Mon, 21 Dec 15 17:00:20 +0000", false},
		{"Thu, 4 Feb 16 14:14:32 +0000", false},
		{"Thu, 04 Feb 16 14:14:32 +0000", false},
	} {
		if _, err := ParseTime(test.input); (err != nil) != test.error {
			t.Errorf("%q want error=%v, got: %v", test.input, test.error, err)
		}
	}
}
