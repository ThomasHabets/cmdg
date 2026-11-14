package main

import (
	"context"
	"os/exec"
	"testing"
)

func TestOpenFileWaitReturnsCommandFailure(t *testing.T) {
	falsePath, err := exec.LookPath("false")
	if err != nil {
		t.Skip("false binary not available")
	}

	oldOpenBinary := *openBinary
	oldOpenWait := *openWait
	defer func() {
		*openBinary = oldOpenBinary
		*openWait = oldOpenWait
	}()
	*openBinary = falsePath
	*openWait = true

	if err := openFile(context.Background(), []byte("attachment"), ".txt"); err == nil {
		t.Fatal("openFile returned nil for failing opener")
	}
}
