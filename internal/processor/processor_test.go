package processor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cfg(t *testing.T) *Config {
	c := Default()
	c.Inbox = filepath.Join(t.TempDir(), "inbox")
	c.Outbox = filepath.Join(t.TempDir(), "outbox")
	c.AllowedOrigins = []string{"ausa-web"}
	c.Handlers = []Handler{{Tag: "#claude", Command: []string{"fake"}, ReplyTo: "ausa-web", ReplyFirstLine: "#email Claude: {subject}", TimeoutSeconds: 5}}
	c.State = filepath.Join(t.TempDir(), "state")
	c.StableSeconds = 1
	c.PollSeconds = 0
	return c
}

func put(t *testing.T, path, content string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Second)
	_ = os.Chtimes(path, old, old)
}

func TestProcessAndReply(t *testing.T) {
	c := cfg(t)
	p, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	var gotStdin string
	p.Exec = func(_ context.Context, cmd []string, stdin string) (string, string, error) {
		gotStdin = stdin
		return "Paris is the capital of France.\n", "", nil
	}
	put(t, filepath.Join(c.Inbox, "ausa-web", "Q-20260917", "message.txt"), "#claude What is the capital of France?\n\nPlease answer briefly.\n")
	put(t, filepath.Join(c.Inbox, "ausa-web", "Q-20260917", "headers.txt"), "From: someone\n")
	put(t, filepath.Join(c.Inbox, "ausa-web", "plain.txt"), "hello, no tag\n")
	put(t, filepath.Join(c.Inbox, "james-pc", "sneaky.txt"), "#claude do something\n")

	res := p.Once()
	st := map[string]Result{}
	for _, r := range res {
		st[filepath.Base(r.File)] = r
	}
	if r := st["message.txt"]; r.Status != "processed" {
		t.Fatalf("message.txt: %+v", r)
	}
	if r := st["sneaky.txt"]; r.Status != "rejected" || !strings.Contains(r.Reason, "not allowed") {
		t.Fatalf("sneaky: %+v", r)
	}
	if _, ok := st["plain.txt"]; ok {
		t.Fatal("plain file processed")
	}
	if gotStdin != "What is the capital of France?\n\nPlease answer briefly." {
		t.Fatalf("stdin: %q", gotStdin)
	}
	replies, _ := filepath.Glob(filepath.Join(c.Outbox, "ausa-web", "claude-reply-*.txt"))
	if len(replies) != 1 {
		t.Fatalf("replies: %v", replies)
	}
	b, _ := os.ReadFile(replies[0])
	lines := strings.SplitN(string(b), "\n", 3)
	if lines[0] != "#email Claude: What is the capital of France?" || !strings.Contains(string(b), "Paris is the capital") || !strings.Contains(string(b), "Request from ausa-web") {
		t.Fatalf("reply:\n%s", b)
	}
	// second pass: nothing new, and a restart remembers too
	if res := p.Once(); len(res) != 0 {
		t.Fatalf("reprocessed: %+v", res)
	}
	p2, _ := New(c, nil)
	p2.Exec = p.Exec
	if res := p2.Once(); len(res) != 0 {
		t.Fatalf("state not persisted: %+v", res)
	}
}

func TestFailuresAndLimits(t *testing.T) {
	c := cfg(t)
	c.Handlers[0].MaxOutputKB = 1
	p, _ := New(c, nil)
	calls := 0
	p.Exec = func(_ context.Context, cmd []string, stdin string) (string, string, error) {
		calls++
		if strings.Contains(stdin, "fail") {
			return "", "boom", errors.New("exit status 1")
		}
		return strings.Repeat("x", 4000), "", nil
	}
	put(t, filepath.Join(c.Inbox, "ausa-web", "a.txt"), "#claude please fail\n")
	put(t, filepath.Join(c.Inbox, "ausa-web", "b.txt"), "#claude long answer\n")
	put(t, filepath.Join(c.Inbox, "ausa-web", "c.txt"), "#claude\n")
	res := p.Once()
	status := map[string]Result{}
	for _, r := range res {
		status[filepath.Base(r.File)] = r
	}
	if status["a.txt"].Status != "failed" || !strings.Contains(status["a.txt"].Reason, "boom") {
		t.Fatalf("a: %+v", status["a.txt"])
	}
	if status["b.txt"].Status != "processed" {
		t.Fatalf("b: %+v", status["b.txt"])
	}
	if status["c.txt"].Status != "rejected" {
		t.Fatalf("c: %+v", status["c.txt"])
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
	replies, _ := filepath.Glob(filepath.Join(c.Outbox, "ausa-web", "claude-reply-*.txt"))
	if len(replies) != 2 {
		t.Fatalf("replies: %v (error report + truncated answer expected)", replies)
	}
	var sawErr, sawTrunc bool
	for _, r := range replies {
		b, _ := os.ReadFile(r)
		if strings.HasPrefix(string(b), "#email Claude: Error:") && strings.Contains(string(b), "boom") {
			sawErr = true
		}
		if strings.Contains(string(b), "[output truncated]") {
			sawTrunc = true
		}
	}
	if !sawErr || !sawTrunc {
		t.Fatalf("err=%v trunc=%v", sawErr, sawTrunc)
	}
	// a failed note is never retried (commands may have side effects)
	if res := p.Once(); len(res) != 0 {
		t.Fatalf("retried: %+v", res)
	}
	// timeouts are reported
	c2 := cfg(t)
	c2.Handlers[0].TimeoutSeconds = 1
	p2, _ := New(c2, nil)
	p2.Exec = func(ctx context.Context, cmd []string, stdin string) (string, string, error) {
		<-ctx.Done()
		return "", "", errors.New("timed out")
	}
	put(t, filepath.Join(c2.Inbox, "ausa-web", "slow.txt"), "#claude slow\n")
	r := p2.Once()
	if len(r) != 1 || r[0].Status != "failed" || !strings.Contains(r[0].Reason, "timed out") {
		t.Fatalf("timeout: %+v", r)
	}
}

func TestValidate(t *testing.T) {
	c := Default()
	if err := c.Validate(); err == nil {
		t.Fatal("empty accepted")
	}
	c.Inbox, c.Outbox, c.AllowedOrigins = "/i", "/o", []string{"x"}
	c.Handlers = []Handler{{Tag: "claude", Command: []string{"x"}, ReplyTo: "y"}}
	if err := c.Validate(); err == nil {
		t.Fatal("tag without # accepted")
	}
	c.Handlers[0].Tag = "#claude"
	c.Handlers[0].ReplyTo = "Bad Id"
	if err := c.Validate(); err == nil {
		t.Fatal("bad reply_to accepted")
	}
	c.Handlers[0].ReplyTo = "y"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Handlers[0].TimeoutSeconds != 300 || c.Handlers[0].ReplyFirstLine != "Re: {subject}" {
		t.Fatalf("defaults: %+v", c.Handlers[0])
	}
}
