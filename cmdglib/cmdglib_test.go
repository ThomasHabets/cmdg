package cmdglib

import (
	"testing"
)

func TestDateFormats(t *testing.T) {
	for _, test := range []struct{
		input  string
		error  bool
	}{
		{"Mon, 21 Dec 15 17:00:20 +0000", false},
	}{
		if _, err := ParseTime(test.input); (err != nil) == test.error {
			t.Errorf("%q want error=%v, got: %v", test.error, err)
		}
	}
}
