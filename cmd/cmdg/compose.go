package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/input"
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

	return compose(ctx, conn, keys, cmdg.NewThread, prefill)
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

// compose() is used for compose, replies, and forwards.
func compose(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, threadID cmdg.ThreadID, prefill string) error {
	for {
		// Get message content.
		msg, err := getInput(ctx, prefill, keys)
		if err != nil {
			return err
		}

		// Ask to send it.
		sendQ := []dialog.Option{
			{Key: "s", Label: "s — Send"},
			{Key: "d", Label: "d — Save as draft"},
			{Key: "a", Label: "a — Abort, discarding draft"},
			{Key: "r", Label: "r — Return to editor"},
		}
		// TODO: send signed.
		// TODO: attach.

		a, err := dialog.Question("Send message?", sendQ, keys)
		if err != nil {
			return err
		}

		switch a {
		case "r": // Return to editor.
			prefill = msg
			continue
		case "^C", "a": // Abandon.
			return nil
		case "s", "S":
			for {
				st := time.Now()
				mp := "mixed"

				head, part, err := cmdg.ParseUserMessage(msg)
				if err != nil {
					// TODO: ask to retry
					return errors.Wrapf(err, "failed to parse that message")
				}

				parts := []*cmdg.Part{part}

				// Add signature.
				if *enableSign {
					sig, err := createSig(ctx, part.FullString())
					if err != nil {
						// TODO: ask to retry or something
						return errors.Wrapf(err, "failed to sign")
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

				if err := conn.SendParts(ctx, threadID, mp, head, parts); err != nil {
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
		default:
			return fmt.Errorf("can't happen! Got %q from compose question", a)
		}
	}
}
