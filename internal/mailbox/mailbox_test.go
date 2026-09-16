package mailbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"a.txt":               "a.txt",
		"docs\\report.pdf":    "docs/report.pdf",
		"/abs/path.txt":       "abs/path.txt",
		"./x/./y.txt":         "x/y.txt",
		"bad:name?.txt":       "bad_name_.txt",
		"CON.txt":             "_CON.txt",
		"trailing. ":          "trailing",
		"  spaced name.docx ": "spaced name.docx",
	}
	for in, want := range cases {
		got, err := CleanName(in)
		if err != nil || got != want {
			t.Errorf("CleanName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "../etc/passwd", "a/../b", "/", "..."} {
		if got, err := CleanName(bad); err == nil && bad != "..." {
			t.Errorf("CleanName(%q) = %q, expected error", bad, got)
		}
	}
}

func TestReceiveUniqueAndHold(t *testing.T) {
	StableAge = 0
	m, err := Open(filepath.Join(t.TempDir(), "mb"))
	if err != nil {
		t.Fatal(err)
	}
	p1, err := m.Receive("alice", "notes/hello.txt", strings.NewReader("one"), 3)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := m.Receive("alice", "notes/hello.txt", strings.NewReader("two"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p1) != "hello.txt" || filepath.Base(p2) != "hello (2).txt" {
		t.Fatalf("unique naming: %s %s", p1, p2)
	}
	if b, _ := os.ReadFile(p2); string(b) != "two" {
		t.Fatalf("content: %q", b)
	}
	if _, err := m.Receive("alice", "short.bin", strings.NewReader("abc"), 10); err == nil {
		t.Fatal("short body should fail")
	}
	if n, _ := filepath.Glob(filepath.Join(m.Dir(DirInbox), "alice", tmpPrefix+"*")); len(n) != 0 {
		t.Fatalf("temp files left behind: %v", n)
	}

	hp, err := m.Hold("bob", "alice", "x/y.txt", strings.NewReader("held"), 4, Meta{ID: "m1", Hops: 1, ReceivedFrom: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hp + MetaExt); err != nil {
		t.Fatal("meta sidecar missing")
	}
	held := m.Held("bob")
	if len(held) != 1 || held[0].Name != "x/y.txt" || held[0].Hops != 1 || held[0].ID != "m1" || !held[0].Held {
		t.Fatalf("held: %+v", held)
	}
	if err := m.Remove(held[0]); err != nil {
		t.Fatal(err)
	}
	if len(m.HeldAll()) != 0 {
		t.Fatal("not removed")
	}
	if _, err := os.Stat(filepath.Join(m.Dir(DirForward), "bob", "alice")); !os.IsNotExist(err) {
		t.Fatal("empty origin folder should be pruned")
	}
}

func TestOutgoingAndMoves(t *testing.T) {
	StableAge = 0
	m, err := Open(filepath.Join(t.TempDir(), "mb"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(m.Dir(DirOutbox), "carol", "sub")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644)
	_ = os.WriteFile(filepath.Join(m.Dir(DirOutbox), "carol", "ignored.tmp"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(m.Dir(DirOutbox), "carol", ".hidden"), []byte("x"), 0o644)
	items, err := m.Outgoing()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].To != "carol" || items[0].Name != "sub/f.txt" {
		t.Fatalf("outgoing: %+v", items)
	}
	dest, err := m.MarkSent(items[0])
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(dest) != filepath.ToSlash(filepath.Join(m.Dir(DirSent), "carol", "sub", "f.txt")) {
		t.Fatalf("sent to %s", dest)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("empty outbox subfolder should be pruned")
	}
	if _, err := os.Stat(filepath.Join(m.Dir(DirOutbox), "carol")); err != nil {
		t.Fatal("outbox/<to> itself must survive")
	}

	_ = os.WriteFile(filepath.Join(m.Dir(DirOutbox), "carol", "g.txt"), []byte("hi"), 0o644)
	items, _ = m.Outgoing()
	fd, err := m.Fail(items[0], "no such node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fd + ".error.txt"); err != nil {
		t.Fatal("error note missing")
	}
	in, out, fw := m.Counts()
	if in != 0 || out != 0 || fw != 0 {
		t.Fatalf("counts %d %d %d", in, out, fw)
	}
}
