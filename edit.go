package main

//
// This file contain functions related to editing messages.
//

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"
)

const (
	savedDir                 = "saved"
	savedDirMode os.FileMode = 0700
)

// saveFailedSend saves emails that fail to send, so that data is not lost.
// Sort of a local drafts folder.
//
// It can't send to cloud because the reason it failed to send may be
// that the network is down or OAuth is broken.
//
// TODO: It would be nice if this were encrypted with a key that *is*
// in the cloud though.
func saveFailedSend(msg string) error {
	dir := path.Join(*configDir, savedDir)
	if err := os.MkdirAll(dir, savedDirMode); err != nil {
		log.Printf("Failed to create %q, continuing anyway: %v", dir, err)
	}

	// In case directory already existed, but with wrong permissions.
	if err := os.Chmod(dir, savedDirMode); err != nil {
		log.Printf("Failed to chmod %q, continuing anyway: %v", dir, err)
	}

	f, err := ioutil.TempFile(dir, "saved-")
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write([]byte(msg)); err != nil {
		// TODO: Is there anything better we can do? Try again?
		return fmt.Errorf("error saving failsafe file. Data lost: %v", err)
	}
	if err := f.Close(); err != nil {
		// TODO: Is there anything better we can do? Try again?
		return fmt.Errorf("error saving failsafe file. Data lost: %v", err)
	}
	return nil
}

// runEditor runs the editor on the input and returns post-editor data.
func runEditor(input string) (string, error) {
	f, err := ioutil.TempFile("", "cmdg-")
	if err != nil {
		return "", fmt.Errorf("creating tempfile: %v", err)
	}
	f.Close()
	defer os.Remove(f.Name())

	if err := ioutil.WriteFile(f.Name(), []byte(input), 0600); err != nil {
		return "", err
	}

	// Re-acquire terminal when done.
	defer runSomething()()

	// Run editor.
	cmd := exec.Command(editorBinary, f.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to open editor %q: %v", editorBinary, err)
	}

	// Read back reply.
	data, err := ioutil.ReadFile(f.Name())
	if err != nil {
		return "", fmt.Errorf("reading back editor output: %v", err)
	}
	return string(data), nil
}

// runEditorHeadersOK is a poorly named function that calls runEditor() until the reply looks somewhat like an email.
func runEditorHeadersOK(input string) (string, error) {
	var s string
	for {
		var err error
		s, err = runEditor(input)
		if err != nil {
			nc.Status("Running editor failed: %v", err)
			return "", err
		}
		s2 := strings.SplitN(s, "\n\n", 2)
		if len(s2) != 2 {
			// TODO: Ask about reopening editor.
			nc.Status("Malformed email, reopening editor")
			input = s
			continue
		}
		break
	}
	return s, nil
}
