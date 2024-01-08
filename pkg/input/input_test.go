package input

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDuration(t *testing.T) {
	for _, test := range []struct {
		i time.Duration
		o unix.Timeval
	}{
		{i: time.Second, o: unix.Timeval{Sec: 1, Usec: 0}},
		{i: time.Millisecond, o: unix.Timeval{Sec: 0, Usec: 1000}},
	} {
		if got := *duration2Timeval(test.i); got != test.o {
			t.Errorf("got %v want %v", got, test.o)
		}
	}
}

func TestMaxTimeoutSpace(t *testing.T) {
	now := time.Now()
	timeout := time.Duration(100)
	deadline := now.Add(time.Duration(500))
	if got, want := maxTimeoutNow(now, deadline, timeout), time.Duration(100); got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
func TestMaxTimeoutNoSpace(t *testing.T) {
	now := time.Now()
	timeout := time.Duration(700)
	deadline := now.Add(time.Duration(500))
	if got, want := maxTimeoutNow(now, deadline, timeout), time.Duration(500); got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
func TestMaxTimeoutNegative(t *testing.T) {
	now := time.Now()
	timeout := time.Duration(100)
	deadline := now.Add(-time.Duration(100))
	if got, want := maxTimeoutNow(now, deadline, timeout), time.Duration(0); got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
