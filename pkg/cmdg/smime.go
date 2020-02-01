package cmdg

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/gpg"
)

// try verifying any signatures.
// CALLED WITH MUTEX HELD
func (msg *Message) trySMIMESigned(ctx context.Context) error {
	log.Infof("Checking SMIME…")
	raw, err := msg.rawNoLock(ctx)
	if err != nil {
		return errors.Wrapf(err, "fetching raw message")
	}

	// TODO: create a FIFO instead?
	f, err := ioutil.TempFile("", "")
	if err != nil {
		return errors.Wrapf(err, "creating temp file for smime check")
	}
	if err := f.Close(); err != nil {
		return errors.Wrapf(err, "failed to close tempfile %q right after closing: %v", f.Name(), err)
	}
	defer func() {
		if err := os.Remove(f.Name()); err != nil {
			log.Errorf("Failed to delete temp file %q with signed cert: %v", f.Name(), err)
		}
	}()

	// TODO: maybe use -out to make extra sure entire content is signed?
	cmd := exec.CommandContext(ctx, Openssl, "cms", "-verify", "-verify_retcode", "-signer", f.Name())
	cmd.Stdin = strings.NewReader(raw)
	var ebuf bytes.Buffer
	cmd.Stderr = &ebuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("signature verification failed: %q", ebuf.String())
	}
	log.Infof("Signature verification succeeded!")
	b, err := ioutil.ReadFile(f.Name())
	if err != nil {
		return errors.Wrapf(err, "failed to read signer's cert from %q", f.Name())
	}
	p, rest := pem.Decode(b)
	if len(rest) != 0 || p == nil {
		return errors.Wrapf(err, "failed to decode signer's PEM. p=%v, len(rest)=%v", p, rest)
	}
	cert, err := x509.ParseCertificate(p.Bytes)
	if err != nil {
		return errors.Wrapf(err, "failed to parse signer's cert")
	}
	log.Infof("Signed: %+v", cert.Subject)
	log.Infof("Issuer: %+v", cert.Issuer)
	msg.gpgStatus = &gpg.Status{
		GoodSignature: true,
		Signed:        unprintableRE.ReplaceAllString(cert.Subject.String(), ""),
	}
	return nil
}
