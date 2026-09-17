package gateway

import (
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func outCfg(t *testing.T) *Config {
	c := Default()
	c.Outbox = filepath.Join(t.TempDir(), "outbox")
	c.Inbox = filepath.Join(t.TempDir(), "inbox")
	c.EmailFrom = "info@ausa.dev"
	c.EmailTo = []string{"austin_armas@live.com"}
	c.EmailAllowTo = []string{"austin_armas@live.com", "james@example.com"}
	c.EmailOrigins = []string{"austin-pc"}
	c.SendCommand = []string{"/nonexistent/sendmail"}
	c.EmailReceipt = true
	c.EmailStableSec = 1
	c.State = filepath.Join(t.TempDir(), "state")
	c.PollSeconds = 0
	return c
}

func write(t *testing.T, path, content string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := timeNowMinus(10)
	_ = os.Chtimes(path, old, old)
}

func TestOutboundPlainAndAttachments(t *testing.T) {
	c := outCfg(t)
	g, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sentMsgs [][]byte
	var sentTo [][]string
	g.Send = func(_ context.Context, msg []byte, to []string) (string, error) {
		sentMsgs = append(sentMsgs, msg)
		sentTo = append(sentTo, to)
		return "msg-id-1", nil
	}
	// plain note
	write(t, filepath.Join(c.Inbox, "austin-pc", "note.txt"), "\n#email Hello from AMail: café\n\nFirst line of the body.\nSecond line.\n")
	// a message folder with attachments (and a headers.txt that must not be attached)
	dir := filepath.Join(c.Inbox, "austin-pc", "Photos-20260917")
	write(t, filepath.Join(dir, "message.txt"), "#email to:james@example.com Photos for you\nSee attached.\n")
	write(t, filepath.Join(dir, "a.png"), "\x89PNGdata")
	write(t, filepath.Join(dir, "headers.txt"), "should not be attached")
	// ordinary files are ignored, and an unauthorised origin is refused
	write(t, filepath.Join(c.Inbox, "austin-pc", "plain.txt"), "just a file\n#email not on first line\n")
	write(t, filepath.Join(c.Inbox, "james-pc", "sneaky.txt"), "#email I am not allowed\nbody\n")

	res := g.Once()
	statuses := map[string]string{}
	for _, r := range res {
		statuses[filepath.Base(r.File)] = r.Status + ":" + r.Reason
	}
	if !strings.HasPrefix(statuses["note.txt"], "sent") || !strings.HasPrefix(statuses["message.txt"], "sent") {
		t.Fatalf("statuses: %v", statuses)
	}
	if !strings.Contains(statuses["sneaky.txt"], "not allowed") {
		t.Fatalf("unauthorised origin accepted: %v", statuses)
	}
	if _, ok := statuses["plain.txt"]; ok {
		t.Fatalf("plain file treated as e-mail: %v", statuses)
	}
	if len(sentMsgs) != 2 {
		t.Fatalf("sent %d messages", len(sentMsgs))
	}
	// files are processed in sorted order; find each message by recipient
	byTo := map[string]int{}
	for i, raw := range sentMsgs {
		mm, err := mail.ReadMessage(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		byTo[mm.Header.Get("To")] = i
	}
	iPlain, iFolder := byTo["austin_armas@live.com"], byTo["james@example.com"]
	// plain note
	m, err := mail.ReadMessage(strings.NewReader(string(sentMsgs[iPlain])))
	if err != nil {
		t.Fatal(err)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if subj != "Hello from AMail: café" || m.Header.Get("From") != "info@ausa.dev" || m.Header.Get("To") != "austin_armas@live.com" || m.Header.Get("X-Amail-Origin") != "austin-pc" {
		t.Fatalf("headers: %v", m.Header)
	}
	body, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, &b64Cleaner{r: m.Body}))
	if !strings.HasPrefix(string(body), "First line of the body.\nSecond line.") {
		t.Fatalf("body: %q", body)
	}
	if sentTo[iPlain][0] != "austin_armas@live.com" {
		t.Fatalf("to: %v", sentTo[iPlain])
	}
	// message 2: override recipient + attachment, headers.txt excluded
	m2, _ := mail.ReadMessage(strings.NewReader(string(sentMsgs[iFolder])))
	if m2.Header.Get("To") != "james@example.com" || sentTo[iFolder][0] != "james@example.com" {
		t.Fatalf("override to: %v %v", m2.Header.Get("To"), sentTo[iFolder])
	}
	mt, params, _ := mime.ParseMediaType(m2.Header.Get("Content-Type"))
	if mt != "multipart/mixed" {
		t.Fatalf("content type %s", mt)
	}
	mr := multipart.NewReader(m2.Body, params["boundary"])
	var names []string
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		if fn := part.FileName(); fn != "" {
			names = append(names, fn)
			data, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, &b64Cleaner{r: part}))
			if fn == "a.png" && string(data) != "\x89PNGdata" {
				t.Fatal("attachment content")
			}
		}
	}
	if len(names) != 1 || names[0] != "a.png" {
		t.Fatalf("attachments: %v", names)
	}
	// receipts written for the origin
	rec, _ := filepath.Glob(filepath.Join(c.Outbox, "austin-pc", "email-receipt-*.txt"))
	if len(rec) != 2 {
		t.Fatalf("receipts: %v", rec)
	}
	// second pass sends nothing again
	if res := g.Once(); len(res) != 0 {
		t.Fatalf("re-sent: %+v", res)
	}
}

func TestOutboundGuards(t *testing.T) {
	c := outCfg(t)
	g, _ := New(c, nil)
	calls := 0
	g.Send = func(_ context.Context, msg []byte, to []string) (string, error) { calls++; return "x", nil }
	// recipient not on the allow-list
	write(t, filepath.Join(c.Inbox, "austin-pc", "r.txt"), "#email to:stranger@evil.example hi\nbody\n")
	// too big
	big := filepath.Join(c.Inbox, "austin-pc", "Big-1", "message.txt")
	write(t, big, "#email big\nbody\n")
	write(t, filepath.Join(c.Inbox, "austin-pc", "Big-1", "blob.bin"), strings.Repeat("x", 8<<20))
	res := g.Once()
	reasons := ""
	for _, r := range res {
		reasons += r.Status + ":" + r.Reason + "\n"
	}
	if calls != 0 || !strings.Contains(reasons, "email_allowed_to") || !strings.Contains(reasons, "limit") {
		t.Fatalf("calls=%d reasons=%s", calls, reasons)
	}
	// a failing send command keeps the file for retry (not remembered)
	c2 := outCfg(t)
	g2, _ := New(c2, nil)
	write(t, filepath.Join(c2.Inbox, "austin-pc", "n.txt"), "#email retry me\nbody\n")
	r1 := g2.Once() // real command /nonexistent/sendmail fails
	if len(r1) != 1 || r1[0].Status != "error" {
		t.Fatalf("expected error result, got %+v", r1)
	}
	g2.Send = func(_ context.Context, msg []byte, to []string) (string, error) { return "ok", nil }
	r2 := g2.Once()
	if len(r2) != 1 || r2[0].Status != "sent" {
		t.Fatalf("expected retry to send, got %+v", r2)
	}
	// delete-after-send removes the file
	c3 := outCfg(t)
	c3.EmailDelete = true
	g3, _ := New(c3, nil)
	g3.Send = func(_ context.Context, msg []byte, to []string) (string, error) { return "ok", nil }
	p := filepath.Join(c3.Inbox, "austin-pc", "gone.txt")
	write(t, p, "#email bye\nbody\n")
	g3.Once()
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file not deleted after send")
	}
}

func TestOutboundConfigValidate(t *testing.T) {
	c := Default()
	c.Inbox, c.SendCommand = "/in", []string{"/usr/sbin/sendmail"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "email_from") {
		t.Fatalf("missing from/to accepted: %v", err)
	}
	c.EmailFrom, c.EmailTo = "a@b.c", []string{"x@y.z"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "email_origins") {
		t.Fatalf("open origins accepted: %v", err)
	}
	c.EmailOrigins = []string{"pc"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.EmailMaxMB != 7 || len(c.EmailAllowTo) != 1 {
		t.Fatalf("defaults: %+v", c.Outbound)
	}
}

func timeNowMinus(sec int) time.Time { return time.Now().Add(-time.Duration(sec) * time.Second) }
