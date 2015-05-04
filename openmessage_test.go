package main

/*
 *  Copyright (C) 2015 Thomas Habets <thomas@habets.se>
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
 */

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
