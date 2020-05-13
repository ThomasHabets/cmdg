package dialog

import (
	"reflect"
	"testing"
)

func TestTrimOneChar(t *testing.T) {
	for _, test := range []struct {
		in  string
		out string
	}{
		{"", ""},
		{"a", ""},
		{"ab", "a"},
		{"fö", "f"},
		{"för", "fö"},
		{"ಠ_ಠ", "ಠ_"},
	} {
		if got, want := TrimOneChar(test.in), test.out; got != want {
			t.Errorf("For %q got %q, want %q", test.in, got, want)
		}
	}
}

func TestFilterSubmatch(t *testing.T) {
	a := &Option{Label: "foo"}
	b := &Option{Label: "bar"}

	for _, test := range []struct {
		in     []*Option
		filter string
		out    []*Option
	}{
		{
			in:     []*Option{a, b},
			filter: "",
			out:    []*Option{a, b},
		},
		{
			in:     []*Option{a, b},
			filter: "bice",
			out:    nil,
		},
		{
			in:     []*Option{a, b},
			filter: "fo",
			out:    []*Option{a},
		},
	} {
		if got, want := filterSubmatch(test.in, test.filter), test.out; !reflect.DeepEqual(got, want) {
			t.Errorf("For %q with filter %q got %q, want %q", test.in, test.filter, got, want)
		}
	}
}
