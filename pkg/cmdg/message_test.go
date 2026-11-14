package cmdg

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	gmail "google.golang.org/api/gmail/v1"
)

func TestGetLabelsWithUnread(t *testing.T) {
	conn := &CmdG{
		messageCache: make(map[string]*Message),
		labelCache:   make(map[string]*Label),
	}
	for _, label := range []struct {
		id   string
		name string
	}{
		{id: Inbox, name: "Inbox"},
		{id: Unread, name: "Unread"},
	} {
		conn.LabelCache(&Label{
			ID:       label.id,
			Label:    label.name,
			Response: &gmail.Label{Id: label.id, Name: label.name},
		})
	}
	msg := NewMessageWithResponse(conn, "msg-1", &gmail.Message{
		Id:       "msg-1",
		LabelIds: []string{Inbox, Unread},
	}, LevelMinimal)

	labels, err := msg.GetLabels(context.Background(), false)
	if err != nil {
		t.Fatalf("GetLabels(false): %v", err)
	}
	if got, want := labelIDs(labels), []string{Inbox}; !sameStrings(got, want) {
		t.Fatalf("GetLabels(false) got %v, want %v", got, want)
	}

	labels, err = msg.GetLabels(context.Background(), true)
	if err != nil {
		t.Fatalf("GetLabels(true): %v", err)
	}
	if got, want := labelIDs(labels), []string{Inbox, Unread}; !sameStrings(got, want) {
		t.Fatalf("GetLabels(true) got %v, want %v", got, want)
	}
}

func TestLocalLabelsReturnsCopy(t *testing.T) {
	msg := &Message{
		Response: &gmail.Message{
			LabelIds: []string{Inbox, Unread},
		},
	}

	labels := msg.LocalLabels()
	labels[0] = Trash

	if got, want := msg.Response.LabelIds[0], Inbox; got != want {
		t.Fatalf("LocalLabels allowed caller to mutate cache: got %q, want %q", got, want)
	}
}

func TestRemoveLabelIDLocal(t *testing.T) {
	msg := &Message{
		Response: &gmail.Message{
			LabelIds: []string{Inbox, Unread, Starred},
		},
	}

	msg.RemoveLabelIDLocal(Unread)

	if got, want := msg.Response.LabelIds, []string{Inbox, Starred}; !sameStrings(got, want) {
		t.Fatalf("RemoveLabelIDLocal got %v, want %v", got, want)
	}
}

func TestMakeBodyNestedMultipart(t *testing.T) {
	body := &gmail.MessagePart{
		MimeType: "multipart/mixed",
		Parts: []*gmail.MessagePart{
			{
				MimeType: "multipart/alternative",
				Parts: []*gmail.MessagePart{
					{
						MimeType: "text/plain",
						Body: &gmail.MessagePartBody{
							Data: MIMEEncode("plain body"),
						},
					},
				},
			},
		},
	}

	got, err := makeBody(context.Background(), body, false)
	if err != nil {
		t.Fatalf("makeBody nested multipart: %v", err)
	}
	if want := "plain body"; got != want {
		t.Fatalf("makeBody nested multipart got %q, want %q", got, want)
	}
}

func TestMakeBodySkipsEmptyNestedMultipart(t *testing.T) {
	body := &gmail.MessagePart{
		MimeType: "multipart/mixed",
		Parts: []*gmail.MessagePart{
			{
				MimeType: "multipart/alternative",
			},
			{
				MimeType: "text/plain",
				Body: &gmail.MessagePartBody{
					Data: MIMEEncode("fallback body"),
				},
			},
		},
	}

	got, err := makeBody(context.Background(), body, false)
	if err != nil {
		t.Fatalf("makeBody with empty nested multipart: %v", err)
	}
	if want := "fallback body"; got != want {
		t.Fatalf("makeBody with empty nested multipart got %q, want %q", got, want)
	}
}

func TestMakeBodyNilLeafBody(t *testing.T) {
	_, err := makeBody(context.Background(), &gmail.MessagePart{
		MimeType: "text/plain",
	}, false)
	if err != errNoUsablePart {
		t.Fatalf("makeBody nil leaf body got %v, want %v", err, errNoUsablePart)
	}
}

func TestPreloadSubjectsToleratesRPCError(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests.Add(1)
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Status:     "500 Internal Server Error",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"boom"}}`)),
				Request:    req,
			}, nil
		}),
	}
	conn, err := NewFake(client)
	if err != nil {
		t.Fatalf("NewFake: %v", err)
	}
	page := &Page{
		Messages: []*Message{NewMessage(conn, "msg-1")},
		Response: &gmail.ListMessagesResponse{
			Messages: []*gmail.Message{{Id: "msg-1"}},
		},
	}

	if err := page.PreloadSubjects(context.Background()); err != nil {
		t.Fatalf("PreloadSubjects returned error for transient RPC failure: %v", err)
	}
	if requests.Load() == 0 {
		t.Fatal("PreloadSubjects did not attempt the request")
	}
	if page.Messages[0].HasData(LevelMetadata) {
		t.Fatal("failed preload marked message metadata as loaded")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func labelIDs(labels []*Label) []string {
	var ret []string
	for _, label := range labels {
		ret = append(ret, label.ID)
	}
	return ret
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for n := range a {
		if a[n] != b[n] {
			return false
		}
	}
	return true
}
