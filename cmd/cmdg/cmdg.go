// cmdg is the next generation cmdg.

package main

import (
	"context"
	"flag"
	"syscall"

	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	cfgFile = flag.String("conf", "cmdg.conf", "Config file.")

	conn *cmdg.CmdG
)

func run(ctx context.Context) error {
	keys := input.New()
	if err := keys.Start(); err != nil {
		return err
	}

	v := NewMessageView(ctx, "INBOX", keys)

	v.Run(ctx)
	log.Infof("MessageView returned, stopping keys")
	keys.Stop()
	log.Infof("Shutting down")
	return nil
}

func main() {
	syscall.Umask(0077)
	flag.Parse()

	var err error
	conn, err = cmdg.New(*cfgFile)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	log.Infof("Connected")

	ctx := context.Background()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
