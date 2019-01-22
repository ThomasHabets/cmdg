package gpg

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"syscall"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type Status struct {
	Signed        string
	Encrypted     []string
	GoodSignature bool
	Warnings      []string
}

var (
	goodSignatureRE = regexp.MustCompile(`(?m)^gpg: Good signature from "(.*)"`)
	badSignatureRE  = regexp.MustCompile(`(?m)^gpg: BAD signature from "(.*)"`)
	encryptedRE     = regexp.MustCompile(`(?m)^gpg: encrypted with[^\n]+\n([^\n]+)\n`)
	unprintableRE   = regexp.MustCompile(`[\033\r]`)
)

type GPG struct {
	GPG string
}

func New(gpg string) *GPG {
	return &GPG{
		GPG: gpg,
	}
}

func (gpg *GPG) Decrypt(ctx context.Context, dec string) (string, *Status, error) {
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, gpg.GPG, "--batch", "--no-tty")
	cmd.Stdin = bytes.NewBufferString(dec)
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return "", nil, errors.Wrapf(err, "failed to start gpg (%q)", gpg.GPG)
	}
	if err := cmd.Wait(); err != nil {
		return "", nil, errors.Wrapf(err, "gpg decrypt failed")
	}
	status := &Status{}
	if m := goodSignatureRE.FindStringSubmatch(stderr.String()); m != nil {
		status.Signed = unprintableRE.ReplaceAllString(m[1], "")
		status.GoodSignature = true
	}
	if ms := encryptedRE.FindAllStringSubmatch(stderr.String(), -1); ms != nil {
		for _, m := range ms {
			status.Encrypted = append(status.Encrypted, strings.Trim(unprintableRE.ReplaceAllString(m[1], ""), "\t "))
		}
	}

	return stdout.String(), status, nil
}

func (gpg *GPG) Verify(ctx context.Context, data, sig string) (*Status, error) {
	dir, err := ioutil.TempDir("", "gpg-signature")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	log.Infof("Checking signature with %q…", dir)
	dataFN := path.Join(dir, "data")
	sigFN := path.Join(dir, "data.gpg")
	if err := ioutil.WriteFile(dataFN, []byte(data), 0600); err != nil {
		return nil, err
	}
	if err := ioutil.WriteFile(sigFN, []byte(sig), 0600); err != nil {
		return nil, err
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, gpg.GPG, "--verify", "--no-tty", sigFN, dataFN)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, errors.Wrapf(err, "failed to start gpg (%q)", gpg.GPG)
	}
	status := &Status{}
	goodOrBad := false
	if err := cmd.Wait(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			return nil, errors.Wrapf(err, "gpg verify failed for odd reason. stderr: %q", stderr.String())
		}
		u, ok := e.Sys().(syscall.WaitStatus)
		if !ok {
			return nil, errors.Wrapf(e, "gpg verify failed, and not unix status. stderr: %q", stderr.String())
		}
		if u.ExitStatus() != 1 {
			return nil, errors.Wrapf(e, "gpg verify failed, and not status 1 (was %d). stderr: %q", u.ExitStatus(), stderr.String())
		}
		// Continue since status 1, assume either good or bad signature now.
	}
	if m := badSignatureRE.FindStringSubmatch(stderr.String()); m != nil {
		status.Signed = unprintableRE.ReplaceAllString(m[1], "")
		goodOrBad = true
	}
	if m := goodSignatureRE.FindStringSubmatch(stderr.String()); m != nil {
		status.Signed = unprintableRE.ReplaceAllString(m[1], "")
		status.GoodSignature = true
		goodOrBad = true
	}
	if !goodOrBad {
		return nil, fmt.Errorf("signature not good nor bad. What? %q", stderr.String())
	}
	return status, nil
}

func (gpg *GPG) VerifyInline(ctx context.Context, data string) (*Status, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, gpg.GPG, "--verify", "--no-tty", "-")
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader(data)
	if err := cmd.Start(); err != nil {
		return nil, errors.Wrapf(err, "failed to start gpg (%q)", gpg.GPG)
	}
	status := &Status{}
	goodOrBad := false
	if err := cmd.Wait(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			return nil, errors.Wrapf(err, "gpg verify failed for odd reason. stderr: %q", stderr.String())
		}
		u, ok := e.Sys().(syscall.WaitStatus)
		if !ok {
			return nil, errors.Wrapf(e, "gpg verify failed, and not unix status. stderr: %q", stderr.String())
		}
		if u.ExitStatus() != 1 {
			return nil, errors.Wrapf(e, "gpg verify failed, and not status 1 (was %d). stderr: %q", u.ExitStatus(), stderr.String())
		}
		// Continue since status 1, assume either good or bad signature now.
	}
	if m := badSignatureRE.FindStringSubmatch(stderr.String()); m != nil {
		status.Signed = unprintableRE.ReplaceAllString(m[1], "")
		goodOrBad = true
	}
	if m := goodSignatureRE.FindStringSubmatch(stderr.String()); m != nil {
		status.Signed = unprintableRE.ReplaceAllString(m[1], "")
		status.GoodSignature = true
		goodOrBad = true
	}
	if !goodOrBad {
		return nil, fmt.Errorf("signature not good nor bad. What? %q", stderr.String())
	}
	return status, nil
}
