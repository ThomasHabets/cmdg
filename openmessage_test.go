package main

import (
	"testing"

	gmail "code.google.com/p/google-api-go-client/gmail/v1"
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
	if got, want := s, "Unable to verify anything. Key ID: Unknown. Error: Unknown"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGPGVerifyNotTrusted(t *testing.T) {
	*gpg = "./testdata/gpg_ok.sh"
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
