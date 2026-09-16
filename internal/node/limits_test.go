package node

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amail/internal/mailbox"
)

func TestLimiter(t *testing.T) {
	l := newLimiter(8, 5)
	var rel []func()
	// per-IP concurrent cap is max/4 = 2, raised to the minimum of 4
	for i := 0; i < 4; i++ {
		r, why, _ := l.admit("10.0.0.1")
		if why != "" {
			t.Fatalf("admit %d: %s", i, why)
		}
		rel = append(rel, r)
	}
	if _, why, logIt := l.admit("10.0.0.1"); why == "" || !logIt {
		t.Fatalf("5th concurrent from one ip should be refused and logged: %q %v", why, logIt)
	}
	if _, why, logIt := l.admit("10.0.0.1"); why == "" || logIt {
		t.Fatalf("second refusal in the same minute should be silent: %q %v", why, logIt)
	}
	for _, r := range rel {
		r()
	}
	// Members reconnecting often are never throttled: 100 successful admits in a row.
	for i := 0; i < 100; i++ {
		r, why, _ := l.admit("10.0.0.1")
		if why != "" {
			t.Fatalf("member admit %d refused: %s", i, why)
		}
		r()
	}
	// Strangers: 5 failed handshakes and the address is dropped at accept.
	for i := 0; i < 4; i++ {
		l.strike("10.0.0.9")
	}
	if r, why, _ := l.admit("10.0.0.9"); why != "" {
		t.Fatalf("4 strikes should still be admitted: %s", why)
	} else {
		r()
	}
	l.strike("10.0.0.9")
	if _, why, _ := l.admit("10.0.0.9"); why == "" || !strings.Contains(why, "failed handshakes") {
		t.Fatalf("5 strikes should refuse: %q", why)
	}
	// another ip is unaffected
	if r, why, _ := l.admit("10.0.0.2"); why != "" {
		t.Fatal(why)
	} else {
		r()
	}
	// global cap
	g := newLimiter(2, 100)
	r1, _, _ := g.admit("1.1.1.1")
	r2, _, _ := g.admit("2.2.2.2")
	if _, why, _ := g.admit("3.3.3.3"); why == "" {
		t.Fatal("global cap should refuse")
	}
	r1()
	r2()
	if _, why, _ := g.admit("3.3.3.3"); why != "" {
		t.Fatal(why)
	}
}

func TestListenerRefusesFloodBeforeTLS(t *testing.T) {
	ns := newNet(t, []bool{true})
	n0 := ns[0]
	waitRole(t, n0, RoleServer, "node0")
	addr := n0.node.ListenAddr()
	// per-IP concurrent cap is 16 (max_connections 64 / 4); open 20 raw sockets and never handshake
	var conns []net.Conn
	closedEarly := 0
	for i := 0; i < 20; i++ {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	time.Sleep(300 * time.Millisecond)
	for _, c := range conns {
		_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		buf := make([]byte, 1)
		if _, err := c.Read(buf); err == io.EOF {
			closedEarly++
		}
		_ = c.Close()
	}
	if closedEarly < 4 {
		t.Fatalf("expected at least 4 connections to be closed by the limiter, got %d", closedEarly)
	}
	// 16 sockets reached the handshake and failed it: 16 strikes, under the cap of 20.
	// A real client still works afterwards.
	time.Sleep(300 * time.Millisecond)
	if _, err := n0.node.cli.Status(context.Background(), n0.cfg.Whitelist[0]); err != nil {
		t.Fatalf("status after flood: %v", err)
	}
	// Push the stranger over the cap: plain TCP connects that send garbage.
	for i := 0; i < 8; i++ {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
		_ = c.Close()
	}
	time.Sleep(300 * time.Millisecond)
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("expected the flooding address to be dropped at accept, got %v", err)
	}
	_ = c.Close()
}

func TestMailboxCapAndDiskFloor(t *testing.T) {
	ns := newNet(t, []bool{true, true})
	n0, n1 := ns[0], ns[1]
	waitRole(t, n1, RoleClient, "node0")

	// Simulate a nearly full disk on every node: nothing is accepted.
	orig := mailbox.FreeBytes
	mailbox.FreeBytes = func(string) (int64, error) { return 1 << 20, nil } // 1 MB free < 512 MB floor
	n1.drop(t, "node0", "blocked.txt", "x")
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(inbox(n0, "node1", "blocked.txt")); err == nil {
		t.Fatal("file accepted despite low disk space")
	}
	if _, err := os.Stat(filepath.Join(n1.cfg.Mailbox, mailbox.DirOutbox, "node0", "blocked.txt")); err != nil {
		t.Fatal("file should still wait in outbox (transient refusal)")
	}
	mailbox.FreeBytes = orig
	waitFile(t, inbox(n0, "node1", "blocked.txt"), "x")

	// Mailbox cap: node0 accepts 1 MB in total (received data).
	n0.stop()
	n0.cfg.MaxMailboxMB = 1
	start(t, n0)
	waitRole(t, n0, RoleServer, "node0")
	n1.drop(t, "node0", "a.bin", strings.Repeat("a", 700<<10))
	waitFor(t, "first 700 KB accepted", 15*time.Second, func() bool {
		_, err := os.Stat(inbox(n0, "node1", "a.bin"))
		return err == nil
	})
	n1.drop(t, "node0", "b.bin", strings.Repeat("b", 700<<10))
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(inbox(n0, "node1", "b.bin")); err == nil {
		t.Fatal("second file should exceed the 1 MB mailbox cap")
	}
	if _, err := os.Stat(filepath.Join(n1.cfg.Mailbox, mailbox.DirOutbox, "node0", "b.bin")); err != nil {
		t.Fatal("refused file should wait in outbox, not fail")
	}
	// Free space in the mailbox (user reads and deletes the file) and it goes through.
	if err := os.Remove(inbox(n0, "node1", "a.bin")); err != nil {
		t.Fatal(err)
	}
	n0.node.Mailbox().Usage() // cached; force expiry below
	time.Sleep(100 * time.Millisecond)
	waitFor(t, "second file after cleanup", 45*time.Second, func() bool {
		_, err := os.Stat(inbox(n0, "node1", "b.bin"))
		return err == nil
	})
}
