package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
)

// http handler for gmail send message commands.
type fakeSend struct {
	msg string
}

func (fs *fakeSend) bad(w http.ResponseWriter, f string, args ...interface{}) {
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, f, args...)
}

func (fs *fakeSend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got, want := r.Method, "POST"; got != want {
		fs.bad(w, "bad method. got %q, want %q", got, want)
		return
	}
	if got, want := r.URL.String(), "/gmail/v1/users/me/messages/send?alt=json&prettyPrint=false"; got != want {
		fs.bad(w, "bad URL. got %q, want %q", got, want)
		return
	}
	if err := r.ParseForm(); err != nil {
		fs.bad(w, "failed to parse form: %v", err)
		return
	}
	content, err := ioutil.ReadAll(r.Body)
	if err != nil {
		fs.bad(w, "failed to read body: %v", err)
		return
	}
	type data struct {
		Raw string `json:"raw"`
	}
	var d data
	if err := json.Unmarshal(content, &d); err != nil {
		fs.bad(w, "failed to parse json: %v", err)
		return
	}
	raw, err := cmdg.MIMEDecode(d.Raw)
	if err != nil {
		fs.bad(w, "failed to base64 decode %q: %v", d.Raw, err)
		return
	}
	fs.msg = string(raw)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{ "id": "12345" }`)
}

// net.RoundTripper that rewrites requests to the local fake.
type redirector struct {
	base string
}

func (redir *redirector) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := *r
	t, err := url.Parse(fmt.Sprintf("%s%s", redir.base, r.URL.Path))
	if err != nil {
		return nil, err
	}
	r2.URL.Scheme = t.Scheme
	r2.URL.Host = t.Host
	log.Printf("Rewrote to %q", r2.URL.String())
	return http.DefaultClient.Do(&r2)
}

func crnl(s string) string {
	return strings.Replace(s, "\n", "\r\n", -1)
}

func TestSendMessage(t *testing.T) {
	tests := []struct {
		name        string
		msg         string // This is what comes from $VISUAL
		attachments []*file
		threadID    cmdg.ThreadID
		bad         bool
		matching    *regexp.Regexp
	}{
		{
			name: "Simple",
			msg:  "To: foo@bar.com\nSubject: hello\n\nWorld",
			matching: regexp.MustCompile(crnl(`MIME-Version: 1.0
Subject: hello
To: foo@bar.com
Content-Type: multipart/mixed; boundary="[a-z0-9]+"
Content-Disposition: inline

--[a-z0-9]+
Content-Disposition: inline
Content-Type: text/plain; charset="UTF-8"

World
--[a-z0-9]+--`)),
		},
		{
			name: "Full name",
			msg:  "To: \"Name1 Name2\" <foo2@bar.com>\nSubject: hello\n\nWorld",
			matching: regexp.MustCompile(crnl(`MIME-Version: 1.0
Subject: hello
To: "Name1 Name2" <foo2@bar.com>
Content-Type: multipart/mixed; boundary="[a-z0-9]+"
Content-Disposition: inline

--[a-z0-9]+
Content-Disposition: inline
Content-Type: text/plain; charset="UTF-8"

World
--[a-z0-9]+--`)),
		},
		{
			name: "Full name with unicode",
			msg:  "To: \"Name1 👍 Name2\" <foo3@bar.com>\nSubject: hello\n\nWorld",
			matching: regexp.MustCompile(crnl(`MIME-Version: 1.0
Subject: hello
To: "=\?utf-8\?q\?Name1_=F0=9F=91=8D_Name2\?=" <foo3@bar.com>
Content-Type: multipart/mixed; boundary="[a-z0-9]+"
Content-Disposition: inline

--[a-z0-9]+
Content-Disposition: inline
Content-Type: text/plain; charset="UTF-8"

World
--[a-z0-9]+--`)),
		},
		{
			name: "Simple with CC",
			msg:  "To: foo@bar.com\nCC: baz@baz.com\nSubject: hello\n\nWorld",
			matching: regexp.MustCompile(crnl(`Cc: baz@baz.com
MIME-Version: 1.0
Subject: hello
To: foo@bar.com
Content-Type: multipart/mixed; boundary="[a-z0-9]+"
Content-Disposition: inline

--[a-z0-9]+
Content-Disposition: inline
Content-Type: text/plain; charset="UTF-8"

World
--[a-z0-9]+--`)),
		},
		{
			name: "Simple with unicode CC",
			msg:  "To: foo@bar.com\nCC: \"Name1 👍 Name2\" <baz@baz.com>\nSubject: hello\n\nWorld",
			matching: regexp.MustCompile(crnl(`Cc: "=[?]utf-8[?]q[?]Name1_=F0=9F=91=8D_Name2[?]=" <baz@baz.com>
MIME-Version: 1.0
Subject: hello
To: foo@bar.com
Content-Type: multipart/mixed; boundary="[a-z0-9]+"
Content-Disposition: inline

--[a-z0-9]+
Content-Disposition: inline
Content-Type: text/plain; charset="UTF-8"

World
--[a-z0-9]+--`)),
		},
		{
			name: "Two To's",
			msg:  "To: foo@bar.com, \"Name1 Name2\" <baz@baz.com>\nSubject: hello\n\nWorld",
			matching: regexp.MustCompile(crnl(`MIME-Version: 1.0
Subject: hello
To: foo@bar.com, "Name1 Name2" <baz@baz.com>
Content-Type: multipart/mixed; boundary="[a-z0-9]+"
Content-Disposition: inline

--[a-z0-9]+
Content-Disposition: inline
Content-Type: text/plain; charset="UTF-8"

World
--[a-z0-9]+--`)),
		},
		{
			name: "Two To's, one with unicode",
			msg:  "To: foo@bar.com, \"Name1 👍 Name2\" <baz@baz.com>\nSubject: hello\n\nWorld",
			matching: regexp.MustCompile(crnl(`MIME-Version: 1.0
Subject: hello
To: foo@bar.com, "=\?utf-8\?q\?Name1_=F0=9F=91=8D_Name2\?=" <baz@baz.com>
Content-Type: multipart/mixed; boundary="[a-z0-9]+"
Content-Disposition: inline

--[a-z0-9]+
Content-Disposition: inline
Content-Type: text/plain; charset="UTF-8"

World
--[a-z0-9]+--`)),
		},
	}

	fs := fakeSend{}
	serv := httptest.NewServer(&fs)
	defer serv.Close()

	client := http.Client{
		Transport: &redirector{base: serv.URL},
	}
	c, err := cmdg.NewFake(&client)
	if err != nil {
		log.Fatalf("Setting up fake: %v", err)
	}

	for _, test := range tests {
		ctx := context.Background()
		err := sendMessage(ctx, c, nil, test.msg, test.threadID, test.attachments)
		if test.bad && err == nil {
			t.Errorf("%s: Expected bad, but err==nil", test.name)
			continue
		}
		if !test.bad && err != nil {
			t.Errorf("%s: Expected good, but err: %v", test.name, err)
			continue
		}
		if test.matching != nil && !test.matching.MatchString(fs.msg) {
			t.Errorf("%s: Did not match regex\n%s\n---\n%s", test.name, test.matching, fs.msg)
		}
	}
}
