package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/mail"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

type (
	headOp func(*mail.Header)
)

const (
	signedMultipartType = `signed; micalg=pgp-sha256; protocol="application/pgp-signature"`
)

func getInput(ctx context.Context, prefill string, keys *input.Input) (string, error) {
	tmpf, err := ioutil.TempFile("", "cmdg-")
	if err != nil {
		return "", errors.Wrap(err, "creating tempfile")
	}
	defer func() {
		if err := os.Remove(tmpf.Name()); err != nil {
			// TODO: show in UI.
			log.Errorf("Failed to remove temp compose file %q: %v", tmpf.Name(), err)
		}
	}()
	if _, err := tmpf.Write([]byte(prefill)); err != nil {
		tmpf.Close()
		return "", errors.Wrapf(err, "prefilling compose file %q with %d bytes", tmpf.Name(), len(prefill))
	}
	if err := tmpf.Close(); err != nil {
		return "", errors.Wrapf(err, "closing prefill file %q", tmpf.Name())
	}

	// Stop UI.
	keys.Stop()
	defer keys.Start()

	cmd := exec.CommandContext(ctx, visualBinary, tmpf.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", errors.Wrapf(err, "failed to start editor %q", visualBinary)
	}
	if err := cmd.Wait(); err != nil {
		return "", errors.Wrapf(err, "editor %q failed", visualBinary)
	}

	// Extract content.
	b, err := ioutil.ReadFile(tmpf.Name())
	if err != nil {
		return "", errors.Wrapf(err, "reading compose tempfile %q", tmpf.Name())
	}
	return string(b), nil
}

func composeNew(ctx context.Context, conn *cmdg.CmdG, keys *input.Input) error {
	toOpt, err := dialog.Selection(dialog.Strings2Options(conn.Contacts()), "To> ", true, keys)
	if err == dialog.ErrAborted {
		return nil
	} else if err != nil {
		return err
	}

	to := toOpt.Key
	if strings.EqualFold(to, "me") {
		p, err := conn.GetProfile(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to get own email address")
		}
		to = p.EmailAddress
	}

	var sig string
	if signature != "" {
		sig = "--\n" + signature + "\n"
	}

	prefill := fmt.Sprintf(`To: %s
CC:
Subject:

%s`, to, sig)

	return compose(ctx, conn, nil, keys, cmdg.NewThread, prefill)
}

func createSig(ctx context.Context, msg string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, *gpgFlag, "--no-tty", "--batch", "-s", "-a", "-b")
	cmd.Stdin = strings.NewReader(msg)
	cmd.Stdout = &out
	log.Debugf("Signing %q", msg)
	if err := cmd.Start(); err != nil {
		return "", errors.Wrapf(err, "failed to start gpg (%q)", *gpgFlag)
	}
	if err := cmd.Wait(); err != nil {
		return "", errors.Wrapf(err, "gpg (%q) failed", *gpgFlag)
	}
	return out.String(), nil
}

type preparedMessage struct {
	mp    string
	parts []*cmdg.Part
	head  mail.Header
}

// take message text and attachments, and turn it into mail headers and parts
func prepareMessage(ctx context.Context, msg string, attachments []*file) (*preparedMessage, error) {
	head, part, err := cmdg.ParseUserMessage(msg)
	if err != nil {
		// TODO: ask to retry
		return nil, errors.Wrapf(err, "failed to parse that message")
	}

	parts := []*cmdg.Part{part}
	mp := "mixed"

	// Add signature.
	if *enableSign {
		sig, err := createSig(ctx, part.FullString())
		if err != nil {
			// TODO: ask to retry or something
			return nil, errors.Wrapf(err, "failed to sign")
		}
		if sig != "" {
			parts = append(parts, &cmdg.Part{
				Header: map[string][]string{
					"Content-Type": {`application/pgp-signature; name="signature.asc"`},
				},
				Contents: sig,
			})
			mp = signedMultipartType
		}
	}
	for _, att := range attachments {
		parts = append(parts, &cmdg.Part{
			// TODO: set better content-type.
			Header: map[string][]string{
				"Content-Type":        {fmt.Sprintf("application/octet-stream; name=%q", att.name)},
				"Content-Disposition": {fmt.Sprintf("attachment; filename=%q", att.name)},
			},
			Contents: string(att.content),
		})
	}
	return &preparedMessage{
		head:  head,
		mp:    mp,
		parts: parts,
	}, nil
}

// take message text and attachments, and turn it into mail headers and parts
func sendMessage(ctx context.Context, conn *cmdg.CmdG, headOps []headOp, msg string, threadID cmdg.ThreadID, attachments []*file) error {
	prep, err := prepareMessage(ctx, msg, attachments)
	if err != nil {
		return errors.Wrap(err, "preparing message")
	}
	for _, op := range headOps {
		op(&prep.head)
	}
	return errors.Wrap(conn.SendParts(ctx, threadID, prep.mp, prep.head, prep.parts), "sending parts")
}

// compose() is used for compose, replies, and forwards.
func compose(ctx context.Context, conn *cmdg.CmdG, headOps []headOp, keys *input.Input, threadID cmdg.ThreadID, msg string) error {
	doEdit := true
	var attachments []*file
	for {
		var err error
		if doEdit {
			// Get message content.
			msg, err = getInput(ctx, msg, keys)
			if err != nil {
				return err
			}
		}

		// Ask to send it.
		sendQ := []dialog.Option{
			{Key: "s", Label: "s — Send"},
			{Key: "d", Label: "d — Save as draft"},
			{Key: "a", Label: "a — Abort, discarding draft"},
			{Key: "t", Label: "t — Attach file(s)"},
			{Key: "r", Label: "r — Return to editor"},
		}
		// TODO: send signed.
		// TODO: attach.

		a, err := dialog.Question("Send message?", sendQ, keys)
		if err != nil {
			return err
		}

		// Default to preparing to edit again.
		doEdit = true

		switch a {
		case "r": // Return to editor.
			continue
		case "^C", "a": // Abandon.
			return nil
		case "s", "S":
			for {
				st := time.Now()

				if err := sendMessage(ctx, conn, headOps, msg, threadID, attachments); err != nil {
					log.Errorf("Failed to send: %v", err)
					a, err := dialog.Question(fmt.Sprintf("Failed to send (%q). Save to local file?", err.Error()), []dialog.Option{
						{Key: "y", Label: "Y — Yes, save to local file"},
						{Key: "n", Label: "N — No, discard completely"},
						{Key: "t", Label: "t — Try again"},
					}, keys)
					if errors.Cause(err) == dialog.ErrAborted {
						// No no no, we won't let you passively cancel this. You say y or n.
					} else if err != nil {
						// OK, I give up.
						return err
					}
					switch a {
					case "y":
						f, err := ioutil.TempFile(".", "cmdg-draft-*.txt")
						if err != nil {
							return errors.Wrapf(err, "couldn't open local file")
						}
						if _, err := f.Write([]byte(msg)); err != nil {
							f.Close()
							return errors.Wrapf(err, "couldn't write to local file")
						}
						if err := f.Close(); err != nil {
							return errors.Wrapf(err, "couldn't close local file")
						}
						return nil
					case "n":
						return nil
					case "t":
						// Try again.
					}
				} else {
					log.Infof("Took %v to send message", time.Since(st))
					break
				}
			}
			if a == "S" {
				// TODO: also archive.
			}
			return nil
		case "d":
			st := time.Now()
			if err := conn.MakeDraft(ctx, msg); err != nil {
				// TODO: ask to save on local filesystem.
				return err
			}
			log.Infof("Took %v to make draft", time.Since(st))
			return nil
		case "t":
			f, err := chooseFile(ctx, keys)
			if errors.Cause(err) == dialog.ErrAborted {
				doEdit = false
				break
			}
			if err != nil {
				dialog.Message("Failed to attach", fmt.Sprintf("Failed to attach file: %v", err), keys)
			}
			doEdit = false
			attachments = append(attachments, f)
		default:
			return fmt.Errorf("can't happen! Got %q from compose question", a)
		}
	}
}

type file struct {
	name    string
	content []byte
}

func chooseFile(ctx context.Context, keys *input.Input) (*file, error) {
	startDir := "."
	for {
		log.Infof("Choosing file in %q", startDir)
		fis, err := ioutil.ReadDir(startDir)
		if err != nil {
			return nil, errors.Wrapf(err, "listing directory %q", startDir)
		}
		opts := []*dialog.Option{
			&dialog.Option{
				Key:    "..",
				KeyInt: -1,
				Label:  "..",
			},
		}
		for n, f := range fis {
			label := f.Name()
			if f.Mode().IsDir() {
				label += "/"
			}
			opts = append(opts, &dialog.Option{
				Key:    f.Name(),
				KeyInt: n, // Index into `fis`.
				Label:  label,
			})
		}
		sort.Slice(opts, func(i, j int) bool {
			if opts[i].KeyInt < 0 {
				return true
			}
			if opts[j].KeyInt < 0 {
				return false
			}
			di := fis[opts[i].KeyInt].Mode().IsDir()
			dj := fis[opts[j].KeyInt].Mode().IsDir()
			if di && !dj {
				return true
			}
			if dj && !di {
				return false
			}
			return opts[i].Label < opts[j].Label
		})
		o, err := dialog.Selection(opts, "Attach> ", false, keys)
		if errors.Cause(err) == dialog.ErrAborted {
			return nil, err
		}
		if err != nil {
			return nil, errors.Wrapf(err, "selecting option")
		}
		if o.KeyInt < 0 {
			startDir = path.Clean(path.Join(startDir, ".."))
			continue
		} else if fis[o.KeyInt].Mode().IsDir() {
			startDir = path.Clean(path.Join(startDir, fis[o.KeyInt].Name()))
			continue
		}
		// File chosen.
		full := path.Join(startDir, fis[o.KeyInt].Name())
		// TODO: attach a ReadCloser?
		b, err := ioutil.ReadFile(full)
		return &file{
			name:    fis[o.KeyInt].Name(),
			content: b,
		}, nil
	}
}
