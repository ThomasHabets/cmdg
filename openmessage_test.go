package main

import (
	"testing"

	gmail "google.golang.org/api/gmail/v1"
)

func TestGPGVerifyKeyNotFound(t *testing.T) {
	*gpg = "./testdata/gpg_keynotfound.sh"
	msg := gmail.Message{
		Payload: &gmail.MessagePart{
			Body: &gmail.MessagePartBody{
				Data: mimeEncode("blaha"),
			},
		},
	}
	s, ok := doOpenMessageCmdGPGVerify(&msg, false)
	if ok {
		t.Errorf("Verify succeeded, expected fail")
	}
	if got, want := s, "Unable to verify anything. Key ID: 1343CF44. Error: Can't check signature: public key not found"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGPGVerifyNotTrusted(t *testing.T) {
	*gpg = "./testdata/gpg_not_trusted.sh"
	msg := gmail.Message{
		Payload: &gmail.MessagePart{
			Body: &gmail.MessagePartBody{
				Data: mimeEncode("blaha"),
			},
		},
	}
	s, ok := doOpenMessageCmdGPGVerify(&msg, false)
	if !ok {
		t.Errorf("Verify failed, expected success")
	}
	if got, want := s, "Verify succeeded, but with untrusted key"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGPGVerifyPerfect(t *testing.T) {
	*gpg = "./testdata/gpg_perfect.sh"
	msg := gmail.Message{
		Payload: &gmail.MessagePart{
			Body: &gmail.MessagePartBody{
				Data: mimeEncode("blaha"),
			},
		},
	}
	s, ok := doOpenMessageCmdGPGVerify(&msg, false)
	if !ok {
		t.Errorf("Verify failed, expected success")
	}
	if got, want := s, "Verify succeeded"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGPGVerifyExecFail(t *testing.T) {
	*gpg = "/no/such/binary"
	msg := gmail.Message{
		Payload: &gmail.MessagePart{
			Body: &gmail.MessagePartBody{
				Data: mimeEncode("blaha"),
			},
		},
	}
	s, ok := doOpenMessageCmdGPGVerify(&msg, false)
	if ok {
		t.Errorf("Verify succeeded, expected fail")
	}
	if got, want := s, "Verify failed to execute: fork/exec /no/such/binary: no such file or directory"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
