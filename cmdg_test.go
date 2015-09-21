package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	gmail "google.golang.org/api/gmail/v1"
)

type sortUpdatesByID []listEntry

func (a sortUpdatesByID) Len() int      { return len(a) }
func (a sortUpdatesByID) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a sortUpdatesByID) Less(i, j int) bool {
	return a[i].msg.Id < a[j].msg.Id
}

func TestGetWord(t *testing.T) {
	tests := []struct {
		in string
		w  string
		r  string
	}{
		{"", "", ""},
		{"hello", "hello", ""},
		{" hello", " hello", ""},
		{"hello world", "hello", " world"},
		{" hello world", " hello", " world"},
	}
	for _, test := range tests {
		w, r := getWord(test.in)
		if got, want := w, test.w; got != want {
			t.Errorf("word: got %q, want %q", got, want)
		}
		if got, want := r, test.r; got != want {
			t.Errorf("remaining: got %q, want %q", got, want)
		}
	}
}

func TestBreakLines(t *testing.T) {
	tests := []struct {
		in  []string
		out []string
	}{
		{
			[]string{},
			[]string{},
		},
		{
			[]string{""},
			[]string{""},
		},
		{
			[]string{"  "},
			[]string{""},
		},
		{
			[]string{"hello world", "second line"},
			[]string{"hello world", "second line"},
		},
		{
			//                1         2         3         4         5         6         7         8
			//       12345678901234567890123456789012345678901234567890123456789012345678901234567890
			[]string{
				"buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo",
				"buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffa buffalo",
			},
			[]string{
				"buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo",
				"buffalo",
				"buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffalo buffa",
				"buffalo",
			},
		},
	}
	for _, test := range tests {
		if got, want := test.out, breakLines(test.in); !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func fakeAPIMeMessages(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Logf("Requested message: %v", r.URL)
	if got, want := r.FormValue("format"), "full"; got != want {
		t.Errorf("Got format=%q, want %q", got, want)
	}
	p := strings.Split(r.URL.Path, "/")
	msgs := map[string]string{
		"id-message-1": `
{
  "id": "id-message-1",
  "threadId": "thread-id-1",
  "labelIds": [ "CATEGORY_FORUMS", "UNREAD", "Label_31" ],
  "historyId": "12345",
  "payload": {
    "partId": "",
    "mimeType": "text/plain",
    "filename": "",
    "headers": [
      { "name": "Delivered-To", "value": "foo@bar.com" },
      { "name": "Subject", "value": "OMG Lolz" }
    ],
    "body": {
      "size": 100,
      "data": "blahencoded"
    }
  }
}`,
		"id-message-2": `
{
  "id": "id-message-2",
  "threadId": "thread-id-1",
  "labelIds": [ "CATEGORY_FORUMS", "UNREAD", "Label_31" ],
  "historyId": "12346",
  "payload": {
    "partId": "",
    "mimeType": "text/plain",
    "filename": "",
    "headers": [
      { "name": "Delivered-To", "value": "foo@bar.com" },
      { "name": "Subject", "value": "Re: OMG Lolz" }
    ],
    "body": {
      "size": 200,
      "data": "blahencodedResponse"
    }
  }
}`,
	}
	if _, err := w.Write([]byte(msgs[p[len(p)-1]])); err != nil {
		t.Errorf("Failed to write profile response: %v", err)
	}
}

func fakeAPIMeHistory(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Logf("Requested history message: %v", r.URL)
	historyID, err := strconv.Atoi(r.FormValue("startHistoryId"))
	if err != nil {
		t.Fatalf("historyID not uint64: %v", err)
	}
	if historyID == 0 {
		t.Errorf("Got historyID=%v, want !=0", historyID)
	}
	if _, err := w.Write([]byte(`{
  "history": [
    { "id": "1" },
    { "id": "12345", "messages": [ {"id": "id-message-1" } ] }
  ],
  "historyId": "12346"
}`)); err != nil {
		t.Errorf("Failed to write profile response: %v", err)
	}
}

func TestListMessages(t *testing.T) {
	sleep = func(time.Duration) {}
	var err error
	gmailService, err = gmail.New(http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Unknown URL requested: %v", r.URL)
	})
	mux.HandleFunc("/me/profile", func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.FormValue("alt"), "json"; got != want {
			t.Errorf("Got alt=%q, want %q", got, want)
		}
		if _, err := w.Write([]byte(`{
  "emailAddress": "foo@bar.com",
  "messagesTotal": 100,
  "threadsTotal": 10,
  "historyId": "12345"
}`)); err != nil {
			t.Errorf("Failed to write profile response: %v", err)
		}
	})
	mux.HandleFunc("/me/messages", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Requested: %v", r.URL)
		if got, want := r.FormValue("alt"), "json"; got != want {
			t.Errorf("Got alt=%q, want %q", got, want)
		}
		if got, want := r.FormValue("pageToken"), ""; got != want {
			t.Errorf("Got pageToken=%q, want %q", got, want)
		}
		if _, err := w.Write([]byte(`{
  "messages": [
    { "id": "id-message-1", "threadId": "thread-id-1" },
    { "id": "id-message-2", "threadId": "thread-id-1" }
  ],
  "nextPageToken": "",
  "resultSizeEstimate": 20
}
}`)); err != nil {
			t.Errorf("Failed to write profile response: %v", err)
		}
	})
	mux.HandleFunc("/me/messages/", func(w http.ResponseWriter, r *http.Request) { fakeAPIMeMessages(t, w, r) })
	mux.HandleFunc("/me/history", func(w http.ResponseWriter, r *http.Request) { fakeAPIMeHistory(t, w, r) })

	ts := httptest.NewServer(mux)
	defer ts.Close()

	for _, historyID := range []uint64{0, 100} {
		gmailService.BasePath = ts.URL
		newHistoryID, msgs, more, errs := list("", "", "", 100, historyID)
		if len(errs) != 0 {
			t.Fatalf("Listing emails: %+v", errs)
		}
		// History API not called when historyID is 0
		if historyID == 0 {
			if got, want := newHistoryID, uint64(0); got != want {
				t.Errorf("history ID: got %v, want %v", got, want)
			}
		} else {
			if got, want := newHistoryID, uint64(12346); got != want {
				t.Errorf("history ID: got %v, want %v", got, want)
			}
		}
		if got, want := len(msgs), 2; got != want {
			t.Fatalf("got %d messages, want %d", got, want)
		}
		if got, want := msgs[0].msg, (&gmail.Message{
			HistoryId: 0,
			Id:        "id-message-1",
			ThreadId:  "thread-id-1",
		}); !reflect.DeepEqual(got, want) {
			t.Fatalf("Message 0: got %+v messages, want %+v", got, want)
		}
		var updates []listEntry
		for m := range more {
			updates = append(updates, m)
		}

		want := []*gmail.Message{
			&gmail.Message{
				HistoryId: 12345,
				LabelIds:  []string{"CATEGORY_FORUMS", "UNREAD", "Label_31"},
				Id:        "id-message-1",
				ThreadId:  "thread-id-1",
			},
			&gmail.Message{
				HistoryId: 12346,
				LabelIds:  []string{"CATEGORY_FORUMS", "UNREAD", "Label_31"},
				Id:        "id-message-2",
				ThreadId:  "thread-id-1",
			},
		}
		if got, want := len(updates), len(want); got != want {
			t.Fatalf("got %d updates, want %d", got, want)
		}
		sort.Sort(sortUpdatesByID(updates))
		for n := range updates {
			updates[n].msg.Payload = nil
			if got, want := updates[n].msg, want[n]; !reflect.DeepEqual(got, want) {
				t.Errorf("update %d: got %+v, want %+v", n, got, want)
			}
		}
	}
}

func TestBackoff(t *testing.T) {
	for n := 0; ; n++ {
		d, done := backoff(n)
		if int64(d) < 0 {
			t.Errorf("backoff %d: sleep time must be postive. Was %v", n, d)
		}
		if got, want := d, 10; int64(got)/1000000000 > int64(want) {
			t.Errorf("backoff %d: too long: got %v, want max %ds", n, got, want)
		}
		if done {
			break
		}
	}
}
