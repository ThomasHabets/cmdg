// cmdg is the next generation cmdg.

package main

import (
	"context"
	"flag"
	"syscall"

	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/gpg"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	cfgFile = flag.String("conf", "cmdg.conf", "Config file.")
	gpgFlag = flag.String("gpg", "gpg", "Path to GnuPG.")

	conn *cmdg.CmdG
)

func run(ctx context.Context) error {
	keys := input.New()
	if err := keys.Start(); err != nil {
		return err
	}

	v := NewMessageView(ctx, "INBOX", keys)

	if err := v.Run(ctx); err != nil {
		log.Errorf("Bailing due to error: %v", err)
	}
	log.Infof("MessageView returned, stopping keys")
	keys.Stop()
	log.Infof("Shutting down")
	return nil
}

func main() {
	syscall.Umask(0077)
	flag.Parse()
	ctx := context.Background()
	cmdg.GPG = gpg.New(*gpgFlag)

	var err error
	conn, err = cmdg.New(*cfgFile)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	log.Infof("Connected")
	if err := conn.LoadLabels(ctx); err != nil {
		log.Fatalf("Loading labels: %v", err)
	}
	log.Infof("Labels loaded")

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
