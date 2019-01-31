package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

func continueDraft(ctx context.Context, conn *cmdg.CmdG, keys *input.Input) error {
	drafts, err := conn.ListDrafts(ctx)
	if err != nil {
		return errors.Wrap(err, "listing drafts")
	}

	var opts []*dialog.Option
	for n, d := range drafts {
		to, err := d.GetHeader(ctx, "To")
		if err != nil {
			to = "<no recipient>"
		}

		subj, err := d.GetHeader(ctx, "Subject")
		if err != nil {
			subj = "<no subject>"
		}

		opts = append(opts, &dialog.Option{
			Key:    d.ID,
			KeyInt: n,
			Label:  fmt.Sprintf("To:%s Subj:%s", to, subj),
		})
	}

	dOpt, err := dialog.Selection(opts, "Draft (NOT IMPLEMENTED YET)> ", false, keys)
	if err == dialog.ErrAborted {
		return nil
	} else if err != nil {
		return errors.Wrap(err, "dialog failed")
	}
	log.Infof("Draft selected: %s %q", dOpt.Key, dOpt)
	draft := drafts[dOpt.KeyInt]

	contents, err := draft.GetBody(ctx)
	if err != nil {
		return errors.Wrap(err, "getting draft body")
	}

	var headers []string
	keep := map[string]bool{
		"To":      true,
		"Cc":      true,
		"Subject": true,
	}
	for _, h := range draft.Response.Message.Payload.Headers {
		if keep[h.Name] {
			headers = append(headers, fmt.Sprintf("%s: %s", h.Name, h.Value))
		}
	}

	prefill := strings.Join(headers, "\n") + "\n\n" + contents
	var msg string
outfor:
	for {
		msg, err = getInput(ctx, prefill, keys)
		if err != nil {
			return err
		}

		// Ask to send it.
		sendQ := []dialog.Option{
			{Key: "s", Label: "s — Send"},
			{Key: "d", Label: "d — Save as draft"},
			{Key: "a", Label: "a — Abort, discarding changes to draft"},
			{Key: "r", Label: "r — Return to editor"},
		}

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
		case "s":
			break outfor
		case "d":
			if err := draft.Update(ctx, fixSubject(msg)); err != nil {
				// TODO: allow option to save to local file.
				return errors.Wrap(err, "updating draft failed")
			}
			return nil
		}
	}

	return nil
}
