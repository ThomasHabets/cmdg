package dialog

import (
	"testing"
)

func TestSplitLastToken(t *testing.T) {
	for _, test := range []struct {
		in     string
		prefix string
		last   string
	}{
		{"", "", ""},
		{"user", "", "user"},
		{"user1, user", "user1, ", "user"},
		{"user1;user", "user1;", "user"},
		{"user1, ", "user1, ", ""},
		{"user1,  user2", "user1,  ", "user2"},
	} {
		prefix, last := splitLastToken(test.in)
		if prefix != test.prefix || last != test.last {
			t.Errorf("For %q got prefix=%q, last=%q; want prefix=%q, last=%q", test.in, prefix, last, test.prefix, test.last)
		}
	}
}

func TestValidateEmails(t *testing.T) {
	for _, test := range []struct {
		in    string
		valid bool
	}{
		{"", true},
		{"foo@bar.com", true},
		{"foo@bar.com, baz@qux.com", true},
		{"foo@bar.com; baz@qux.com", true},
		{"foo@bar.com, invalid", false},
		{"invalid; foo@bar.com", false},
		{"foo@bar.com, ", true}, // Trailing delimiter is OK? Or should be false?
		// Actually, standard email inputs often allow trailing commas while typing.
		// But for final submission, maybe it's fine.
		{"  foo@bar.com  ", true},
	} {
		err := validateEmails(test.in)
		if (err == nil) != test.valid {
			t.Errorf("For %q got valid=%v, want valid=%v. Error: %v", test.in, err == nil, test.valid, err)
		}
	}
}
