// cmdg is a command-line GMail client.
/*
 *  Copyright (C) 2015-2019 Thomas Habets <thomas@habets.se>
 *
 *  This software is dual-licensed GPL and "Thomas is allowed to release a
 *  binary version that adds shared API keys and nothing else".
 *
 *  This program is free software; you can redistribute it and/or modify
 *  it under the terms of the GNU General Public License as published by
 *  the Free Software Foundation; either version 2 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU General Public License for more details.
 *
 *  You should have received a copy of the GNU General Public License along
 *  with this program; if not, write to the Free Software Foundation, Inc.,
 *  51 Franklin Street, Fifth Floor, Boston, MA 02110-1301 USA.
 *
 * Some more interesting stuff can be found in doc for:
 *  golang.org/x/text/encoding
 *  golang.org/x/text/encoding/charmap
 */
package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"sync"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/gpg"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

const (
	version           = "1.0-beta"
	signatureFilename = "signature.txt"
)

var (
	license         = flag.Bool("license", false, "Show program license.")
	cfgFile         = flag.String("config", "", "Config file. Default is ~/"+path.Join(defaultConfigDir, configFileName))
	gpgFlag         = flag.String("gpg", "gpg", "Path to GnuPG.")
	logFile         = flag.String("log", "/dev/null", "Log debug data to this file.")
	configure       = flag.Bool("configure", false, "Configure OAuth.")
	updateSignature = flag.Bool("update_signature", false, "Upload ~/.signature to app settings.")
	verbose         = flag.Bool("verbose", false, "Turn on verbose logging.")
	versionFlag     = flag.Bool("version", false, "Show version and exit.")

	conn *cmdg.CmdG

	// Relative to configDir.
	configFileName = "cmdg.conf"

	// Relative to $HOME.
	defaultConfigDir = ".cmdg"

	pagerBinary  string
	visualBinary string

	labelReloadTime = time.Minute

	signature string
)

func configFilePath() string {
	if *cfgFile != "" {
		return *cfgFile
	}
	return path.Join(os.Getenv("HOME"), defaultConfigDir, configFileName)
}

func loadSignature(ctx context.Context) error {
	b, err := conn.GetFile(ctx, signatureFilename)
	if err == os.ErrNotExist {
		return nil
	}
	if err != nil {
		return err
	}
	signature = string(b)
	return nil
}

func run(ctx context.Context) error {
	keys := input.New()
	if err := keys.Start(); err != nil {
		return err
	}

	v := NewMessageView(ctx, "INBOX", "", keys)

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
	cmdg.Version = version

	if flag.NArg() != 0 {
		log.Fatalf("Trailing args on cmdline: %q", flag.Args())
	}

	if *verbose {
		log.SetLevel(log.DebugLevel)
	}

	if *license {
		fmt.Printf("%s\n", licenseText)
		return
	}

	if *versionFlag {
		fmt.Printf("cmdg %s\nhttps://github.com/ThomasHabets/cmdg", version)
		return
	}

	if *configure {
		if err := cmdg.Configure(configFilePath()); err != nil {
			log.Fatalf("Configuring: %v", err)
		}
		return
	}

	ctx := context.Background()

	pagerBinary = os.Getenv("PAGER")
	if len(pagerBinary) == 0 {
		log.Fatalf("You need to set the PAGER environment variable. When in doubt, set to 'less'.")
	}

	visualBinary = os.Getenv("VISUAL")
	if len(visualBinary) == 0 {
		visualBinary = os.Getenv("EDITOR")
		if len(visualBinary) == 0 {
			log.Fatalf("You need to set the VISUAL or EDITOR environment variable. Set to your favourite editor.")
		}
	}

	cmdg.GPG = gpg.New(*gpgFlag)

	var err error
	conn, err = cmdg.New(configFilePath())
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	log.Infof("Connected")

	if *updateSignature {
		p := path.Join(os.Getenv("HOME"), ".signature")
		b, err := ioutil.ReadFile(p)
		if err != nil {
			log.Fatalf("Reading %q: %v", p, err)
		}
		if err := conn.UpdateFile(ctx, signatureFilename, b); err != nil {
			log.Fatalf("Uploading signature file: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := loadSignature(ctx); err != nil {
			log.Fatalf("Failed to load signature from Drive appdata: %v", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := conn.LoadLabels(ctx); err != nil {
			log.Fatalf("Loading labels: %v", err)
		}
		log.Infof("Labels loaded")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := conn.LoadContacts(ctx); err != nil {
			log.Fatalf("Loading contacts: %v", err)
		}
		log.Infof("Contacts loaded")
	}()
	wg.Wait()

	go func() {
		ch := time.Tick(labelReloadTime)
		for {
			<-ch
			if err := conn.LoadLabels(ctx); err != nil {
				log.Errorf("Loading labels: %v", err)
			} else {
				log.Infof("Reloaded labels")
			}
			if err := conn.LoadContacts(ctx); err != nil {
				log.Errorf("Loading contacts: %v", err)
			} else {
				log.Infof("Reloaded contacts")
			}
		}
	}()

	// Redirect logging.
	{
		f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatalf("Can't create logfile %q: %v", *logFile, err)
		}
		defer f.Close()
		log.SetOutput(f)
		log.SetFormatter(&log.TextFormatter{
			DisableColors: true,
		})
	}

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
