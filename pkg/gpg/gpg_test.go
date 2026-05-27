package gpg

import (
	"bytes"
	"context"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

const (
	testKeyPassphrase = "abc123"
	gpg2              = "gpg2"
)

var (
	gpg = "gpg"
)

func TestMain(m *testing.M) {
	// Check for gpg2 binary, and use that if it exists.
	if err := exec.Command(gpg2, "--version").Run(); err == nil {
		gpg = gpg2
	}

	dir, err := ioutil.TempDir("", "gpg-test")
	if err != nil {
		log.Fatalf("Failed to create tempdir: %v", err)
	}
	os.Setenv("GNUPGHOME", dir)
	defer os.Setenv("GNUPGHOME", "")
	defer os.RemoveAll(dir)

	// Create key.
	cmd := exec.Command(gpg, "--batch", "--import", "test/thomas@habets.se.pub")
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to import author key: %v", err)
	}

	cmd = exec.Command(gpg, "--batch", "--gen-key", "-")
	cmd.Stdin = strings.NewReader(`%echo Generating a basic OpenPGP key
     Key-Type: DSA
     Key-Length: 1024
     Subkey-Type: ELG-E
     Subkey-Length: 1024
     Name-Real: Joe Tester
     Name-Comment: with stupid passphrase
     Name-Email: test@example.com
     Expire-Date: 0
     Passphrase: ` + testKeyPassphrase + `
     # Do a commit here, so that we can later print "done" :-)
     %commit
     %echo done
`)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to generate test key: %v", err)
	}

	os.Exit(m.Run())
}

func TestNoGPG(t *testing.T) {
	gpg := New("/usaoehts")

	ctx := context.Background()

	if _, err := gpg.Verify(ctx, "", ""); err == nil {
		t.Fatal("Bad binary succeesed")
	}

	if _, _, err := gpg.Decrypt(ctx, ""); err == nil {
		t.Fatal("Bad binary succeesed")
	}
}

func TestVerify(t *testing.T) {
	data, err := ioutil.ReadFile("test/detached.txt")
	if err != nil {
		t.Fatalf("Failed to read detached data: %v", err)
	}
	sig, err := ioutil.ReadFile("test/detached.txt.asc")
	if err != nil {
		t.Fatalf("Failed to read detached sig: %v", err)
	}

	for _, test := range []struct {
		name string
		data string
		sig  string
		want *Status
		fail bool
	}{
		{
			name: "good",
			data: string(data),
			sig:  string(sig),
			want: &Status{
				Signed:        "Thomas Habets <thomas@habets.se>",
				GoodSignature: true,
			},
		},
		// TODO: sign with unknown key.
		{
			name: "bad",
			data: string(data) + "blah",
			sig:  string(sig),
			want: &Status{
				Signed:        "Thomas Habets <thomas@habets.se>",
				GoodSignature: false,
			},
		},
		{
			name: "invalid",
			data: string(data),
			sig:  "blah",
			fail: true,
			want: &Status{
				GoodSignature: false,
			},
		},
	} {
		ctx := context.Background()
		g := New(gpg)
		s, err := g.Verify(ctx, test.data, test.sig)
		if test.fail {
			if err == nil {
				t.Errorf("%q: Verify succeeded, expected fail", test.name)
			}
			continue
		} else {
			if err != nil {
				t.Fatalf("%q: Failed to verify: %v", test.name, err)
			}
		}
		log.Print(s)
		if got, want := s, test.want; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %+v, want %+v", test.name, got, want)
		}
	}
}

func TestVerifyInline(t *testing.T) {
	bdata, err := ioutil.ReadFile("test/inline.txt.asc")
	if err != nil {
		t.Fatalf("Failed to read inline msg: %v", err)
	}
	data := string(bdata)

	for _, test := range []struct {
		name string
		data string
		want *Status
		fail bool
	}{
		{
			name: "good",
			data: data,
			want: &Status{
				Signed:        "Thomas Habets <thomas@habets.se>",
				GoodSignature: true,
			},
		},
		{
			name: "corrupt",
			data: strings.Replace(data, "b", "B", -1),
			fail: true,
			want: &Status{
				Signed: "Thomas Habets <thomas@habets.se>",
			},
		},
		// TODO: sign with unknown key.
		// TODO: bad signature
		{
			name: "missing",
			data: "blaha",
			fail: true,
			want: &Status{},
		},
	} {
		ctx := context.Background()
		g := New(gpg)
		s, err := g.VerifyInline(ctx, test.data)
		if test.fail {
			if err == nil {
				t.Errorf("%q: Verify succeeded, expected fail", test.name)
			}
			continue
		} else {
			if err != nil {
				t.Fatalf("%q: Failed to verify: %v", test.name, err)
			}
		}
		if got, want := s, test.want; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %+v, want %+v", test.name, got, want)
		}
	}
}

func TestDecrypt(t *testing.T) {
	ctx := context.Background()

	var enc bytes.Buffer
	cmd := exec.CommandContext(ctx, gpg, "--batch", "-a", "-e", "-r", "test@example.com")
	cmd.Stdin = strings.NewReader("test message")
	cmd.Stdout = &enc
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to encrypt message: %v", err)
	}

	for _, test := range []struct {
		name   string
		data   string
		out    string
		status *Status
		fail   bool
	}{
		{
			name: "good",
			data: enc.String(),
			out:  "test message",
			status: &Status{
				Encrypted: []string{"Joe Tester (with stupid passphrase) <test@example.com>"},
			},
		},
		{
			name:   "corrupt",
			data:   strings.Replace(enc.String(), "b", "B", -1),
			fail:   true,
			status: &Status{},
		},
		// TODO: encrypted with unknown key.
		// TODO: various checks on the signature
		{
			name:   "missing",
			data:   "blaha",
			fail:   true,
			status: &Status{},
		},
	} {
		ctx := context.Background()
		g := New(gpg)
		g.Passphrase = testKeyPassphrase
		out, s, err := g.Decrypt(ctx, test.data)
		if test.fail {
			if err == nil {
				t.Errorf("%q: Verify succeeded, expected fail", test.name)
			}
			continue
		} else {
			if err != nil {
				t.Fatalf("%q: Failed to verify: %v", test.name, err)
			}
		}
		if got, want := out, test.out; got != want {
			t.Errorf("%q: data: got %q, want %q", test.name, got, want)
		}
		if got, want := s, test.status; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: status: got\n%+v\nwant\n%+v", test.name, got, want)
		}
	}
}
