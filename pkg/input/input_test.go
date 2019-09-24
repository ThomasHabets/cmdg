package input

import (
	"testing"
	"time"
)

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
