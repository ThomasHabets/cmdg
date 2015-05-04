package main

import (
	"reflect"
	"testing"
)

func TestGetWord(t *testing.T) {
	tests := []struct {
		in string
		w  string
		r  string
	}{
		{"", "", ""},
		{"hello", "hello", ""},
		{" hello", " hello", ""},
		{"hello world", "hello", " world"},
		{" hello world", " hello", " world"},
	}
	for _, test := range tests {
		w, r := getWord(test.in)
		if got, want := w, test.w; got != want {
			t.Errorf("word: got %q, want %q", got, want)
		}
		if got, want := r, test.r; got != want {
			t.Errorf("remaining: got %q, want %q", got, want)
		}
	}
}

func TestBreakLines(t *testing.T) {
	tests := []struct {
		in  []string
		out []string
	}{
		{
			[]string{},
			[]string{},
		},
		{
			[]string{""},
			[]string{""},
		},
		{
			[]string{"  "},
			[]string{""},
		},
		{
			[]string{"hello world", "second line"},
			[]string{"hello world", "second line"},
		},
		{
			//                1         2         3         4         5         6         7         8
			//       12345678901234567890123456789012345678901234567890123456789012345678901234567890
			[]string{
				"buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo",
				"buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffa buffalo",
			},
			[]string{
				"buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo",
				"buffalo",
				"buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffa",
				"buffalo",
			},
		},
	}
	for _, test := range tests {
		if got, want := test.out, breakLines(test.in); !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
