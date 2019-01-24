package main

import (
	"context"
	"flag"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	openBinary = flag.String("open", "xdg-open", "Command to open attachments with.")
	openWait   = flag.Bool("open_wait", false, "Wait after opening attachment. If using X, then makes sense to say no.")
)

func listAttachments(ctx context.Context, keys *input.Input, msg *cmdg.Message) error {
	as, err := msg.Attachments(ctx)
	if err != nil {
		return err
	}
	ass := make([]string, len(as), len(as))
	for n, a := range as {
		ass[n] = a.Part.Filename
	}
	which, err := dialog.Selection(dialog.Strings2Options(ass), false, keys)

	chosen := as[which.KeyInt]

	for {
		sendQ := []dialog.Option{
			{Key: "s", Label: "Save"},
			{Key: "o", Label: "Open"},
			{Key: "a", Label: "Abort"},
		}
		a, err := dialog.Question(sendQ, keys)
		if err != nil {
			return err
		}

		switch a {
		case "a": // Abort
			return nil
		case "o": // Open
			// TODO: show download status
			data, err := chosen.Download(ctx)
			if err != nil {
				return err
			}
			return openFile(ctx, data, path.Ext(chosen.Part.Filename))
		case "s":
			// TODO: show download status
			data, err := chosen.Download(ctx)
			if err != nil {
				return err
			}
			return saveFile(ctx, data, chosen.Part.Filename)
		}

	}
}

func saveFile(ctx context.Context, data []byte, fn string) error {
	// TODO.
	return nil
}

func openFile(ctx context.Context, data []byte, ext string) error {
	f, err := ioutil.TempFile("", "cmdg-attachment-*"+ext)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		if err := os.Remove(f.Name()); err != nil {
			log.Errorf("Failed to remove tempfile after failure %q: %v", f.Name(), err)
		}
		return err
	}
	if err := f.Close(); err != nil {
		if err := os.Remove(f.Name()); err != nil {
			log.Errorf("Failed to remove tempfile after failure %q: %v", f.Name(), err)
		}
		return err
	}
	fn := f.Name()
	cmd := exec.CommandContext(ctx, *openBinary, fn)
	if *openWait {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return errors.Wrapf(err, "failed to start binary %q", *openBinary)
	}
	w := func() {
		if err := cmd.Wait(); err != nil {
			log.Errorf("Failed to finish opening attachment %q using %q: %v", fn, *openBinary, err)
		}
		if !*openWait {
			// Some application openers run in the background, so keep the file around for a bit.
			time.Sleep(time.Minute)
		}
		if err := os.Remove(fn); err != nil {
			log.Errorf("Failed to remove tempfile %q: %v", fn, err)
		}
	}
	if *openWait {
		w()
	} else {
		go w()
	}
	return nil

}
