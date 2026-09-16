package node

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amail/internal/config"
	"amail/internal/keys"
	"amail/internal/mailbox"
)

type testNode struct {
	id     string
	home   string
	cfg    *config.Config
	node   *Node
	cancel context.CancelFunc
	done   chan struct{}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// newNet builds n nodes on loopback. hosted[i] decides whether node i has a
// host entry in the shared whitelist (i.e. can be reached / be the server).
func newNet(t *testing.T, hosted []bool) []*testNode {
	t.Helper()
	mailbox.StableAge = 0
	root := t.TempDir()
	caDir := filepath.Join(root, "ca")
	if err := keys.InitCA(caDir, "testnet"); err != nil {
		t.Fatal(err)
	}
	var wl []config.Peer
	ports := make([]int, len(hosted))
	for i, h := range hosted {
		ports[i] = freePort(t)
		p := config.Peer{ID: fmt.Sprintf("node%d", i)}
		if h {
			p.Host, p.Port = "127.0.0.1", ports[i]
		}
		wl = append(wl, p)
	}
	var nodes []*testNode
	for i := range hosted {
		id := fmt.Sprintf("node%d", i)
		home := filepath.Join(root, id)
		b, err := keys.Issue(caDir, id, 30, "test")
		if err != nil {
			t.Fatal(err)
		}
		if err := keys.Install(home, b); err != nil {
			t.Fatal(err)
		}
		mat, err := keys.Load(home)
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.Default(home, id)
		cfg.Listen = fmt.Sprintf("127.0.0.1:%d", ports[i])
		cfg.Mailbox = filepath.Join(home, "mailbox")
		cfg.PollSeconds, cfg.DiscoverySeconds, cfg.MaxFileMB = 1, 1, 1
		cfg.Whitelist = append([]config.Peer(nil), wl...)
		nodes = append(nodes, &testNode{id: id, home: home, cfg: cfg})
		_ = mat
	}
	for _, tn := range nodes {
		start(t, tn)
	}
	t.Cleanup(func() {
		for _, tn := range nodes {
			tn.stop()
		}
	})
	return nodes
}

func start(t *testing.T, tn *testNode) {
	mat, err := keys.Load(tn.home)
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(os.Stdout, "["+tn.id+"] ", log.Ltime|log.Lmicroseconds)
	n, err := New(Options{Config: tn.cfg, Material: mat, Logger: logger, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	n.cli.Timeout = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	tn.node, tn.cancel, tn.done = n, cancel, make(chan struct{})
	go func() {
		defer close(tn.done)
		_ = n.Run(ctx)
	}()
}

func (tn *testNode) stop() {
	if tn.cancel != nil {
		tn.cancel()
		<-tn.done
		tn.cancel = nil
	}
}

func (tn *testNode) drop(t *testing.T, to, name, content string) {
	t.Helper()
	p := filepath.Join(tn.cfg.Mailbox, mailbox.DirOutbox, to, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitFile(t *testing.T, path, want string) {
	t.Helper()
	waitFor(t, "file "+path, 15*time.Second, func() bool {
		b, err := os.ReadFile(path)
		return err == nil && string(b) == want
	})
}

func inbox(tn *testNode, from, name string) string {
	return filepath.Join(tn.cfg.Mailbox, mailbox.DirInbox, from, filepath.FromSlash(name))
}

func waitRole(t *testing.T, tn *testNode, role, leader string) {
	t.Helper()
	waitFor(t, tn.id+" role "+role+"/"+leader, 15*time.Second, func() bool {
		r, l := tn.node.Role()
		return r == role && l == leader
	})
}

func TestNetworkDeliveryAndRelay(t *testing.T) {
	// node0 and node1 are reachable; node2 is behind NAT (no host).
	ns := newNet(t, []bool{true, true, false})
	n0, n1, n2 := ns[0], ns[1], ns[2]

	waitRole(t, n0, RoleServer, "node0")
	waitRole(t, n1, RoleClient, "node0")
	waitRole(t, n2, RoleClient, "node0")

	// direct: client -> server
	n1.drop(t, "node0", "hello.txt", "hi from node1")
	waitFile(t, inbox(n0, "node1", "hello.txt"), "hi from node1")
	waitFor(t, "sent copy", 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(n1.cfg.Mailbox, mailbox.DirSent, "node0", "hello.txt"))
		return err == nil
	})

	// direct: server -> client, with a sub folder
	n0.drop(t, "node1", "docs/report.txt", "report body")
	waitFile(t, inbox(n1, "node0", "docs/report.txt"), "report body")

	// NAT node sends directly to a hosted node
	n2.drop(t, "node1", "from-nat.txt", "nat says hi")
	waitFile(t, inbox(n1, "node2", "from-nat.txt"), "nat says hi")

	// hosted client -> NAT node: relayed through the server, pulled by node2
	n1.drop(t, "node2", "relayed.txt", "via server")
	waitFile(t, inbox(n2, "node1", "relayed.txt"), "via server")
	waitFor(t, "server forward folder emptied", 10*time.Second, func() bool {
		return len(n0.node.Mailbox().HeldAll()) == 0
	})

	// server -> NAT node: held locally until node2 pulls
	n0.drop(t, "node2", "held.txt", "held by server")
	waitFile(t, inbox(n2, "node0", "held.txt"), "held by server")

	// duplicate names get a (2)
	n1.drop(t, "node0", "hello.txt", "second hello")
	waitFile(t, inbox(n0, "node1", "hello (2).txt"), "second hello")

	// unknown destination -> failed/
	n1.drop(t, "nobody", "lost.txt", "x")
	waitFor(t, "failed folder", 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(n1.cfg.Mailbox, mailbox.DirFailed, "nobody", "lost.txt.error.txt"))
		return err == nil
	})

	// status over the wire from a client
	st, err := n1.node.cli.Status(context.Background(), n1.cfg.Whitelist[0])
	if err != nil {
		t.Fatal(err)
	}
	if st.NodeID != "node0" || st.Role != RoleServer || st.Network != "testnet" {
		t.Fatalf("status: %+v", st)
	}
	peers, err := n1.node.cli.Peers(context.Background(), n1.cfg.Whitelist[0])
	if err != nil || len(peers) != 3 {
		t.Fatalf("peers: %v %+v", err, peers)
	}
}

func TestFailoverWhenServerStops(t *testing.T) {
	ns := newNet(t, []bool{true, true, false})
	n0, n1, n2 := ns[0], ns[1], ns[2]
	waitRole(t, n1, RoleClient, "node0")
	n0.stop()
	waitRole(t, n1, RoleServer, "node1")
	waitRole(t, n2, RoleClient, "node1")

	// mail still flows through the new server
	n2.drop(t, "node1", "after.txt", "still works")
	waitFile(t, inbox(n1, "node2", "after.txt"), "still works")

	// node0 comes back and takes over again (it is first on the whitelist)
	start(t, n0)
	waitRole(t, n0, RoleServer, "node0")
	waitRole(t, n1, RoleClient, "node0")
}

func TestBlacklistAndWhitelist(t *testing.T) {
	ns := newNet(t, []bool{true, true})
	n0, n1 := ns[0], ns[1]
	waitRole(t, n1, RoleClient, "node0")

	n0.stop()
	n0.cfg.Blacklist = []config.Block{{ID: "node1", Reason: "test"}}
	start(t, n0)
	waitRole(t, n0, RoleServer, "node0")

	_, err := n1.node.cli.Status(context.Background(), n1.cfg.Whitelist[0])
	if err == nil || !strings.Contains(err.Error(), "blacklisted") {
		t.Fatalf("expected blacklist rejection, got %v", err)
	}

	// A node without a key from this network cannot connect at all.
	other := filepath.Join(t.TempDir(), "other")
	if err := keys.InitCA(filepath.Join(other, "ca"), "othernet"); err != nil {
		t.Fatal(err)
	}
	b, _ := keys.Issue(filepath.Join(other, "ca"), "node1", 30, "x")
	_ = keys.Install(other, b)
	mat, _ := keys.Load(other)
	c := NewClient(mat)
	c.Timeout = 2 * time.Second
	if _, err := c.Status(context.Background(), n1.cfg.Whitelist[0]); err == nil {
		t.Fatal("foreign key should be refused")
	}
}

func TestTooLarge(t *testing.T) {
	ns := newNet(t, []bool{true, true})
	n0, n1 := ns[0], ns[1]
	waitRole(t, n1, RoleClient, "node0")
	big := strings.Repeat("x", 2<<20) // 2 MB > max_file_mb 1
	n1.drop(t, "node0", "big.bin", big)
	waitFor(t, "big file rejected", 15*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(n1.cfg.Mailbox, mailbox.DirFailed, "node0", "big.bin.error.txt"))
		return err == nil
	})
	_ = n0
}
