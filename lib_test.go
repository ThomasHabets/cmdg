package main

import (
	"testing"
)

func TestParallelSimple(t *testing.T) {
	p := &parallel{}
	var res1, res2 int
	p.add(func(ch chan<- func()) {
		ch <- func() {
			if got, want := 0, res2; got != want {
				t.Errorf("Res2 got %d before func1, want %d", got, want)
			}
			res1 = 1
		}
	})
	p.add(func(ch chan<- func()) {
		ch <- func() {
			if got, want := 1, res1; got != want {
				t.Errorf("Res1 got %d before func2, want %d", got, want)
			}
			res2 = 2
		}
	})
	if got, want := 0, res1; got != want {
		t.Errorf("Res1 got %d before run, want %d", got, want)
	}
	if got, want := 0, res2; got != want {
		t.Errorf("Res2 got %d before run, want %d", got, want)
	}
	p.run()
	if got, want := 1, res1; got != want {
		t.Errorf("Res1 got %d, want %d", got, want)
	}
	if got, want := 2, res2; got != want {
		t.Errorf("Res1 got %d, want %d", got, want)
	}
}

func TestParallelNil(t *testing.T) {
	p := &parallel{}
	p.add(func(ch chan<- func()) { close(ch) })
	p.run()
}
