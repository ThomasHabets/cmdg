package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	gmail "google.golang.org/api/gmail/v1"
)

type mockGmailHandler struct {
	sentMsg string
}

func (h *mockGmailHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("MOCK RECEIVED: %s %s\n", r.Method, r.URL.Path)
	if r.Method == "POST" && strings.Contains(r.URL.Path, "/messages/send") {
		content, _ := ioutil.ReadAll(r.Body)
		var d struct {
			Raw string `json:"raw"`
		}
		json.Unmarshal(content, &d)
		raw, _ := base64.URLEncoding.DecodeString(d.Raw)
		h.sentMsg = string(raw)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id": "sent-123"}`)
		return
	}
	if r.Method == "GET" && strings.Contains(r.URL.Path, "/messages/msg-123") && !strings.Contains(r.URL.Path, "/attachments") {
		msg := &gmail.Message{
			Id: "msg-123",
			Payload: &gmail.MessagePart{
				Parts: []*gmail.MessagePart{
					{
						MimeType: "text/plain",
						Body: &gmail.MessagePartBody{
							Data: base64.URLEncoding.EncodeToString([]byte("Original body")),
						},
					},
					{
						Filename: "test.txt",
						MimeType: "text/plain",
						Headers: []*gmail.MessagePartHeader{
							{Name: "Content-Disposition", Value: "attachment; filename=\"test.txt\""},
						},
						Body: &gmail.MessagePartBody{
							AttachmentId: "att-123",
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(msg)
		return
	}
	if r.Method == "GET" && strings.Contains(r.URL.Path, "/attachments/att-123") {
		att := &gmail.MessagePartBody{
			Data: base64.URLEncoding.EncodeToString([]byte("attachment content")),
		}
		json.NewEncoder(w).Encode(att)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

type mockRedirector struct {
	base string
}

func (r *mockRedirector) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := *req
	t, _ := url.Parse(fmt.Sprintf("%s%s", r.base, req.URL.Path))
	r2.URL.Scheme = t.Scheme
	r2.URL.Host = t.Host
	return http.DefaultClient.Do(&r2)
}

func TestForward_Extraction(t *testing.T) {
	h := &mockGmailHandler{}
	ts := httptest.NewServer(h)
	defer ts.Close()

	client := &http.Client{
		Transport: &mockRedirector{base: ts.URL},
	}
	conn, _ := cmdg.NewFake(client)

	// Get a message object
	msg := cmdg.NewMessage(conn, "msg-123")

	// Test extraction logic directly to avoid UI blocking
	atts, err := msg.Attachments(context.Background())
	if err != nil {
		t.Fatalf("Failed to get attachments: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("Expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Part.Filename != "test.txt" {
		t.Errorf("Expected filename test.txt, got %s", atts[0].Part.Filename)
	}

	content, err := atts[0].Download(context.Background())
	if err != nil {
		t.Fatalf("Failed to download attachment: %v", err)
	}
	if string(content) != "attachment content" {
		t.Errorf("Expected 'attachment content', got %q", string(content))
	}
}
