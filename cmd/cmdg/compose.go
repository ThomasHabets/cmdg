package main

import (
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

	// Run $VISUAL. TODO: use $VISUAL
	editor := "emacs"
	cmd := exec.CommandContext(ctx, editor, tmpf.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", errors.Wrapf(err, "failed to start editor %q", editor)
	}
	if err := cmd.Wait(); err != nil {
		return "", errors.Wrap(err, "editor failed")
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
	if err != nil {
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

	signature := "" // TODO
	if signature != "" {
		signature = "--\n" + signature + "\n"
	}

	prefill := fmt.Sprintf(`To: %s
CC:
Subject: 

%s`, to, signature)

	return compose(ctx, conn, keys, prefill)
}

func compose(ctx context.Context, conn *cmdg.CmdG, keys *input.Input, prefill string) error {
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

		a, err := dialog.Question(sendQ, keys)
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
			st := time.Now()
			if err := conn.Send(ctx, msg); err != nil {
				// TODO: ask to save on local filesystem.
				return err
			}
			log.Infof("Took %v to send message", time.Since(st))
			if a == "S" {
				// TODO: also archive.
			}
			return nil
		default:
			return fmt.Errorf("can't happen! Got %q from compose question", a)
		}
	}
	return nil
}
