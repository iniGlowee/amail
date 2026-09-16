// Package node is the AMail daemon: it listens on the canonical port,
// decides whether this machine is the server or a client, moves files out
// of outbox/, holds files for nodes it cannot reach, and pulls its own
// mail from the server.
package node

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"amail/internal/config"
	"amail/internal/keys"
	"amail/internal/mailbox"
	"amail/internal/proto"
)

// Roles a node can be in.
const (
	RoleSearching = "searching"
	RoleServer    = "server"
	RoleClient    = "client"
)

// MaxHops is the most relays a file may pass through.
const MaxHops = 3

// Options configure a Node.
type Options struct {
	Config   *config.Config
	Material *keys.Material
	Logger   *log.Logger
	Version  string
}

// Node is a running AMail node.
type Node struct {
	cfg     *config.Config
	mat     *keys.Material
	mb      *mailbox.Mailbox
	log     *log.Logger
	cli     *Client
	version string
	started time.Time

	mu        sync.Mutex
	role      string
	leader    string
	listening bool
	listenErr string
	ln        net.Listener

	inflight sync.Map // path -> struct{}
	quiet    sync.Map // path -> time.Time of last "pending" log

	histMu sync.Mutex
}

// New builds a node from options.
func New(o Options) (*Node, error) {
	if o.Config == nil || o.Material == nil {
		return nil, errors.New("config and key material are required")
	}
	if err := o.Config.Validate(); err != nil {
		return nil, err
	}
	if o.Config.NodeID != o.Material.NodeID {
		return nil, fmt.Errorf("config node_id %q does not match the installed key (issued to %q)", o.Config.NodeID, o.Material.NodeID)
	}
	mb, err := mailbox.Open(o.Config.Mailbox)
	if err != nil {
		return nil, err
	}
	lg := o.Logger
	if lg == nil {
		lg = log.New(os.Stdout, "", log.LstdFlags)
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	return &Node{
		cfg:     o.Config,
		mat:     o.Material,
		mb:      mb,
		log:     lg,
		cli:     NewClient(o.Material),
		version: o.Version,
		started: time.Now(),
		role:    RoleSearching,
	}, nil
}

// Mailbox exposes the node's mailbox.
func (n *Node) Mailbox() *mailbox.Mailbox { return n.mb }

// Role returns the current role and the id of the node acting as server.
func (n *Node) Role() (string, string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.role, n.leader
}

func (n *Node) setRole(role, leader string) {
	n.mu.Lock()
	changed := n.role != role || n.leader != leader
	n.role, n.leader = role, leader
	n.mu.Unlock()
	if changed {
		switch role {
		case RoleServer:
			n.log.Printf("role: SERVER (no server ahead of me on the whitelist)")
		case RoleClient:
			n.log.Printf("role: client of %s", leader)
		default:
			n.log.Printf("role: searching (no reachable server and this node is not eligible)")
		}
	}
}

// Run starts the listener and loops until ctx is cancelled.
func (n *Node) Run(ctx context.Context) error {
	n.log.Printf("amail %s: node %q on network %q, mailbox %s", n.version, n.cfg.NodeID, n.mat.Network, n.mb.Root)
	if time.Until(n.mat.NotAfter) < 30*24*time.Hour {
		n.log.Printf("WARNING: network key expires %s; ask the operator for a new one", n.mat.NotAfter.Format("2006-01-02"))
	}
	n.startListener(ctx)
	n.discover(ctx)
	n.tick(ctx)

	discovery := time.NewTicker(time.Duration(n.cfg.DiscoverySeconds) * time.Second)
	poll := time.NewTicker(time.Duration(n.cfg.PollSeconds) * time.Second)
	defer discovery.Stop()
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			n.mu.Lock()
			ln := n.ln
			n.mu.Unlock()
			if ln != nil {
				_ = ln.Close()
			}
			n.log.Printf("stopped")
			return nil
		case <-discovery.C:
			n.discover(ctx)
		case <-poll.C:
			n.tick(ctx)
		}
	}
}

// --- listener -------------------------------------------------------------

func (n *Node) startListener(ctx context.Context) {
	ln, err := net.Listen("tcp", n.cfg.Listen)
	n.mu.Lock()
	if err != nil {
		n.listenErr = err.Error()
		n.mu.Unlock()
		n.log.Printf("WARNING: cannot listen on %s: %v (this node can still send and pull as a client)", n.cfg.Listen, err)
		return
	}
	n.ln = ln
	n.listening = true
	n.mu.Unlock()
	n.log.Printf("listening on %s (TLS, network %q)", ln.Addr(), n.mat.Network)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				n.log.Printf("accept: %v", err)
				time.Sleep(200 * time.Millisecond)
				continue
			}
			go n.handle(ctx, c)
		}
	}()
}

// ListenAddr returns the bound address, if listening.
func (n *Node) ListenAddr() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ln == nil {
		return ""
	}
	return n.ln.Addr().String()
}

func (n *Node) handle(ctx context.Context, raw net.Conn) {
	defer raw.Close()
	remote := raw.RemoteAddr().String()
	if blocked, why := n.cfg.IsBlocked("", remote); blocked {
		n.log.Printf("refused %s: %s", remote, why)
		return
	}
	_ = raw.SetDeadline(time.Now().Add(30 * time.Minute))
	tc := tls.Server(raw, n.mat.ServerTLS())
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := tc.HandshakeContext(hctx)
	cancel()
	if err != nil {
		n.log.Printf("refused %s: tls: %v", remote, err)
		return
	}
	peer := keys.PeerID(tc.ConnectionState())
	conn := proto.NewConn(tc)
	if blocked, why := n.cfg.IsBlocked(peer, remote); blocked {
		n.log.Printf("refused %s (%s): %s", peer, remote, why)
		_ = conn.Error(proto.CodeReject, why)
		return
	}
	if !n.allowed(peer) {
		n.log.Printf("refused %s (%s): not on whitelist", peer, remote)
		_ = conn.Error(proto.CodeReject, fmt.Sprintf("%s is not on %s's whitelist", peer, n.cfg.NodeID))
		return
	}
	h, _, err := conn.Recv()
	if err != nil {
		n.log.Printf("%s (%s): bad request: %v", peer, remote, err)
		return
	}
	switch h.Type {
	case proto.TypeStatus:
		_ = conn.Send(&proto.Header{Type: proto.TypeOK, From: n.cfg.NodeID, Status: n.Status()}, nil, 0)
	case proto.TypePeers:
		_ = conn.Send(&proto.Header{Type: proto.TypeOK, From: n.cfg.NodeID, Peers: n.peerInfos()}, nil, 0)
	case proto.TypeDeliver:
		n.handleDeliver(conn, peer, remote, h)
	case proto.TypePull:
		n.handlePull(conn, peer)
	default:
		_ = conn.Error(proto.CodeReject, "unknown request type "+h.Type)
	}
}

// allowed reports whether a network member may talk to us: ourselves, or
// anyone on the whitelist. An empty whitelist accepts every member of the
// network (everyone holding a key from the operator).
func (n *Node) allowed(peer string) bool {
	if peer == "" {
		return false
	}
	if peer == n.cfg.NodeID || len(n.cfg.Whitelist) == 0 {
		return true
	}
	_, ok := n.cfg.Peer(peer)
	return ok
}

func (n *Node) peerInfos() []proto.PeerInfo {
	out := make([]proto.PeerInfo, 0, len(n.cfg.Whitelist))
	for _, p := range n.cfg.Whitelist {
		out = append(out, proto.PeerInfo{ID: p.ID, Host: p.Host, Port: p.Port})
	}
	return out
}

// Status describes this node.
func (n *Node) Status() *proto.Status {
	in, out, fw := n.mb.Counts()
	n.mu.Lock()
	role, leader, listening := n.role, n.leader, n.listening
	n.mu.Unlock()
	return &proto.Status{
		NodeID:    n.cfg.NodeID,
		Network:   n.mat.Network,
		Role:      role,
		Server:    leader,
		Version:   n.version,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Uptime:    time.Since(n.started).Round(time.Second).String(),
		Time:      now(),
		Listening: listening,
		Inbox:     in,
		Outbox:    out,
		Forward:   fw,
	}
}

func (n *Node) handleDeliver(conn *proto.Conn, peer, remote string, h *proto.Header) {
	self := n.cfg.NodeID
	origin := h.Origin
	if origin == "" {
		origin = peer
	}
	if err := config.ValidID(origin); err != nil {
		_ = conn.Error(proto.CodeReject, err.Error())
		return
	}
	if blocked, why := n.cfg.IsBlocked(origin, ""); blocked {
		_ = conn.Error(proto.CodeReject, why)
		return
	}
	name, err := mailbox.CleanName(h.Name)
	if err != nil {
		_ = conn.Error(proto.CodeReject, err.Error())
		return
	}
	if h.Size < 0 || h.Size > n.cfg.MaxBytes() {
		_ = conn.Error(proto.CodeReject, fmt.Sprintf("%s refuses files over %d MB", self, n.cfg.MaxFileMB))
		return
	}
	to := h.To
	if to == "" {
		to = self
	}
	if to != self {
		if h.Hops >= MaxHops {
			_ = conn.Error(proto.CodeReject, fmt.Sprintf("too many hops (%d) for %s", h.Hops, to))
			return
		}
		if _, ok := n.cfg.Peer(to); !ok {
			_ = conn.Error(proto.CodeReject, fmt.Sprintf("%s does not know a node called %q", self, to))
			return
		}
		if blocked, why := n.cfg.IsBlocked(to, ""); blocked {
			_ = conn.Error(proto.CodeReject, why)
			return
		}
	}
	if err := conn.Send(&proto.Header{Type: proto.TypeOK, From: self, Msg: "send body"}, nil, 0); err != nil {
		return
	}
	bh, body, err := conn.Recv()
	if err != nil {
		n.log.Printf("%s: body from %s did not arrive: %v", name, peer, err)
		return
	}
	if bh.Type != proto.TypeBody || body.Len != h.Size {
		_ = conn.Error(proto.CodeReject, "expected a body frame of the announced size")
		return
	}
	id := h.ID
	if id == "" {
		id = newID()
	}
	if to == self {
		path, err := n.mb.Receive(origin, name, body, h.Size)
		if err != nil {
			n.log.Printf("%s: storing %s from %s failed: %v", self, name, origin, err)
			_ = conn.Error(proto.CodeBusy, "could not store file: "+err.Error())
			return
		}
		via := ""
		if peer != origin {
			via = " via " + peer
		}
		n.log.Printf("received %s (%d bytes) from %s%s -> %s", name, h.Size, origin, via, path)
		n.history("received", id, origin, self, peer, name, h.Size, path)
		_ = conn.Send(&proto.Header{Type: proto.TypeOK, From: self, ID: id}, nil, 0)
		return
	}
	path, err := n.mb.Hold(to, origin, name, body, h.Size, mailbox.Meta{ID: id, Hops: h.Hops + 1, ReceivedFrom: peer})
	if err != nil {
		_ = conn.Error(proto.CodeBusy, "could not hold file: "+err.Error())
		return
	}
	n.log.Printf("holding %s (%d bytes) for %s from %s (via %s, hop %d) -> %s", name, h.Size, to, origin, peer, h.Hops+1, path)
	n.history("held", id, origin, to, peer, name, h.Size, path)
	_ = conn.Send(&proto.Header{Type: proto.TypeOK, From: self, ID: id, Msg: "held for " + to}, nil, 0)
}

func (n *Node) handlePull(conn *proto.Conn, peer string) {
	for _, it := range n.mb.Held(peer) {
		if !n.claim(it.Path) {
			continue
		}
		ok := n.pushItem(conn, it)
		n.release(it.Path)
		if !ok {
			return
		}
	}
	_ = conn.Send(&proto.Header{Type: proto.TypeEnd, From: n.cfg.NodeID}, nil, 0)
}

func (n *Node) pushItem(conn *proto.Conn, it mailbox.Item) bool {
	f, err := os.Open(it.Path)
	if err != nil {
		return true // vanished; carry on
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return true
	}
	id := it.ID
	if id == "" {
		id = newID()
	}
	err = conn.Send(&proto.Header{Type: proto.TypeItem, From: n.cfg.NodeID, To: it.To, Origin: it.Origin, ID: id, Name: it.Name, Size: st.Size(), Hops: it.Hops, Time: now()}, f, st.Size())
	_ = f.Close()
	if err != nil {
		n.log.Printf("pull by %s: sending %s failed: %v", it.To, it.Name, err)
		return false
	}
	ah, _, err := conn.Recv()
	if err != nil || ah.Type != proto.TypeAck {
		n.log.Printf("pull by %s: no ack for %s (kept)", it.To, it.Name)
		return false
	}
	_ = n.mb.Remove(it)
	n.log.Printf("delivered %s to %s (pulled, from %s)", it.Name, it.To, it.Origin)
	n.history("delivered", id, it.Origin, it.To, "pull", it.Name, st.Size(), "")
	return true
}

// --- discovery / election -------------------------------------------------

// discover decides the role. Rule: the first *reachable* whitelist entry, in
// list order, is the server. This node counts as reachable to itself when it
// is listening and server_eligible; a node without a host entry can never be
// chosen by others, so it only becomes server if nothing else answers.
func (n *Node) discover(ctx context.Context) {
	self := n.cfg.NodeID
	var before, after []config.Peer
	seenSelf := false
	for _, p := range n.cfg.Whitelist {
		if p.ID == self {
			seenSelf = true
			continue
		}
		if p.Host == "" {
			continue
		}
		if seenSelf {
			after = append(after, p)
		} else {
			before = append(before, p)
		}
	}
	leader := ""
	for _, p := range before {
		if n.reachable(ctx, p) {
			leader = p.ID
			break
		}
	}
	if leader == "" && n.eligible() {
		leader = self
	}
	if leader == "" {
		for _, p := range after {
			if n.reachable(ctx, p) {
				leader = p.ID
				break
			}
		}
	}
	switch leader {
	case "":
		n.setRole(RoleSearching, "")
	case self:
		n.setRole(RoleServer, self)
	default:
		n.setRole(RoleClient, leader)
	}
}

func (n *Node) eligible() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.listening && n.cfg.ServerEligible
}

func (n *Node) reachable(ctx context.Context, p config.Peer) bool {
	if blocked, _ := n.cfg.IsBlocked(p.ID, p.Host); blocked {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, n.cli.Timeout)
	defer cancel()
	st, err := n.cli.Status(cctx, p)
	return err == nil && st.NodeID == p.ID
}

// --- delivery -------------------------------------------------------------

type outcome int

const (
	pending outcome = iota
	delivered
	relayed
	held
)

// fatal marks an error that will not go away by retrying.
type fatal struct{ error }

func (n *Node) tick(ctx context.Context) {
	n.deliverOutbox(ctx)
	n.deliverHeld(ctx)
	n.pull(ctx)
}

func (n *Node) deliverOutbox(ctx context.Context) {
	items, err := n.mb.Outgoing()
	if err != nil {
		n.log.Printf("outbox scan: %v", err)
		return
	}
	self := n.cfg.NodeID
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		it.Origin, it.Hops, it.ID = self, 0, newID()
		if !n.claim(it.Path) {
			continue
		}
		func() {
			defer n.release(it.Path)
			if it.To == self {
				f, err := os.Open(it.Path)
				if err != nil {
					return
				}
				path, err := n.mb.Receive(self, it.Name, f, it.Size)
				_ = f.Close()
				if err == nil {
					_, _ = n.mb.MarkSent(it)
					n.log.Printf("delivered %s to myself -> %s", it.Name, path)
				}
				return
			}
			out, err := n.route(ctx, it)
			n.finish(it, out, err)
		}()
	}
}

func (n *Node) deliverHeld(ctx context.Context) {
	self := n.cfg.NodeID
	for _, it := range n.mb.HeldAll() {
		if ctx.Err() != nil {
			return
		}
		if !n.claim(it.Path) {
			continue
		}
		func() {
			defer n.release(it.Path)
			if it.To == self {
				f, err := os.Open(it.Path)
				if err != nil {
					return
				}
				path, err := n.mb.Receive(it.Origin, it.Name, f, it.Size)
				_ = f.Close()
				if err == nil {
					_ = n.mb.Remove(it)
					n.log.Printf("received %s from %s (was held) -> %s", it.Name, it.Origin, path)
					n.history("received", it.ID, it.Origin, self, "held", it.Name, it.Size, path)
				}
				return
			}
			out, err := n.route(ctx, it)
			n.finish(it, out, err)
		}()
	}
}

// finish applies the result of a route attempt to the item on disk.
func (n *Node) finish(it mailbox.Item, out outcome, err error) {
	var f fatal
	switch {
	case errors.As(err, &f):
		dest, _ := n.mb.Fail(it, err.Error())
		n.log.Printf("FAILED %s to %s: %v -> %s", it.Name, it.To, err, dest)
		n.history("failed", it.ID, it.Origin, it.To, "", it.Name, it.Size, err.Error())
	case out == delivered:
		if it.Held {
			_ = n.mb.Remove(it)
		} else {
			_, _ = n.mb.MarkSent(it)
		}
		n.log.Printf("delivered %s to %s (direct)", it.Name, it.To)
		n.history("delivered", it.ID, it.Origin, it.To, "direct", it.Name, it.Size, "")
	case out == relayed:
		_, leader := n.Role()
		if it.Held {
			_ = n.mb.Remove(it)
		} else {
			_, _ = n.mb.MarkSent(it)
		}
		n.log.Printf("relayed %s for %s via %s", it.Name, it.To, leader)
		n.history("relayed", it.ID, it.Origin, it.To, leader, it.Name, it.Size, "")
	case out == held:
		if !it.Held {
			if _, err := n.mb.HoldFile(it, mailbox.Meta{ID: it.ID, Hops: 0, ReceivedFrom: n.cfg.NodeID}); err != nil {
				n.log.Printf("holding %s for %s failed: %v", it.Name, it.To, err)
				return
			}
			_, _ = n.mb.MarkSent(it)
			n.log.Printf("holding %s for %s until it connects", it.Name, it.To)
			n.history("held", it.ID, it.Origin, it.To, "", it.Name, it.Size, "")
		}
	default:
		if last, ok := n.quiet.Load(it.Path); !ok || time.Since(last.(time.Time)) > 5*time.Minute {
			n.quiet.Store(it.Path, time.Now())
			n.log.Printf("no route yet for %s to %s (will keep trying)", it.Name, it.To)
		}
	}
}

// route tries, in order: straight to the destination, then through the
// server. When this node *is* the server and cannot reach the destination
// it holds the file until that node pulls.
func (n *Node) route(ctx context.Context, it mailbox.Item) (outcome, error) {
	self := n.cfg.NodeID
	peer, known := n.cfg.Peer(it.To)
	if !known {
		return pending, fatal{fmt.Errorf("unknown destination %q: add it with 'amail whitelist add %s <host>'", it.To, it.To)}
	}
	if blocked, why := n.cfg.IsBlocked(it.To, peer.Host); blocked {
		return pending, fatal{errors.New(why)}
	}
	hdr := proto.Header{ID: it.ID, To: it.To, Origin: it.Origin, Name: it.Name, Hops: it.Hops}
	if peer.Host != "" {
		err := n.cli.Deliver(ctx, peer, hdr, it.Path)
		if err == nil {
			return delivered, nil
		}
		if proto.IsReject(err) {
			return pending, fatal{err}
		}
		n.debugf("direct %s -> %s: %v", it.Name, it.To, err)
	}
	role, leader := n.Role()
	if role == RoleServer {
		return held, nil
	}
	if role == RoleClient && leader != it.To && leader != it.ReceivedFrom && leader != self {
		sp, ok := n.cfg.Peer(leader)
		if ok {
			err := n.cli.Deliver(ctx, sp, hdr, it.Path)
			if err == nil {
				return relayed, nil
			}
			if proto.IsReject(err) {
				return pending, fatal{err}
			}
			n.debugf("relay %s via %s: %v", it.Name, leader, err)
		}
	}
	return pending, nil
}

func (n *Node) pull(ctx context.Context) {
	role, leader := n.Role()
	if role != RoleClient {
		return
	}
	sp, ok := n.cfg.Peer(leader)
	if !ok {
		return
	}
	self := n.cfg.NodeID
	got, err := n.cli.Pull(ctx, sp, func(h *proto.Header, body io.Reader) error {
		path, err := n.mb.Receive(h.Origin, h.Name, body, h.Size)
		if err != nil {
			return err
		}
		via := ""
		if h.Origin != leader {
			via = " via " + leader
		}
		n.log.Printf("received %s (%d bytes) from %s%s -> %s", h.Name, h.Size, h.Origin, via, path)
		n.history("received", h.ID, h.Origin, self, leader, h.Name, h.Size, path)
		return nil
	})
	if err != nil {
		n.debugf("pull from %s: %v (got %d)", leader, err, got)
	}
}

// --- helpers --------------------------------------------------------------

func (n *Node) claim(path string) bool {
	_, loaded := n.inflight.LoadOrStore(path, struct{}{})
	return !loaded
}

func (n *Node) release(path string) { n.inflight.Delete(path) }

func (n *Node) debugf(format string, args ...any) {
	if os.Getenv("AMAIL_DEBUG") != "" {
		n.log.Printf("debug: "+format, args...)
	}
}

// history appends one line to <home>/state/history.jsonl.
func (n *Node) history(event, id, from, to, via, name string, size int64, note string) {
	if n.cfg.Home == "" {
		return
	}
	rec := map[string]any{
		"time": now(), "event": event, "id": id, "from": from, "to": to,
		"via": via, "name": name, "size": size, "note": note,
	}
	js, err := json.Marshal(rec)
	if err != nil {
		return
	}
	n.histMu.Lock()
	defer n.histMu.Unlock()
	dir := filepath.Join(n.cfg.Home, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "history.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(js, '\n'))
	_ = f.Close()
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}
