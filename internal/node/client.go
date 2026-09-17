package node

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/iniGlowee/amail/internal/config"
	"github.com/iniGlowee/amail/internal/keys"
	"github.com/iniGlowee/amail/internal/proto"
)

// IdleTimeout closes a connection with no progress for this long.
const IdleTimeout = 90 * time.Second

// Client makes outbound AMail calls to peers.
type Client struct {
	Self    string
	Mat     *keys.Material
	Timeout time.Duration // dial + handshake timeout
	// Revoked, when set, refuses peers presenting a revoked certificate.
	Revoked keys.RevokedFunc
	// Gossip, when set, is attached to every request so peers learn our
	// revocation list; Learn receives theirs from every reply.
	Gossip func() []string
	Learn  func(from string, serials []string)
}

// NewClient returns a client for the given node material.
func NewClient(mat *keys.Material) *Client {
	return &Client{Self: mat.NodeID, Mat: mat, Timeout: 8 * time.Second}
}

func (c *Client) dial(ctx context.Context, peer config.Peer) (*proto.Conn, error) {
	addr := peer.Addr()
	if addr == "" {
		return nil, fmt.Errorf("peer %s has no host", peer.ID)
	}
	d := net.Dialer{Timeout: c.Timeout}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(withIdle(raw, IdleTimeout), c.Mat.ClientTLS(peer.ID, c.Revoked))
	hctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("tls to %s (%s): %w", peer.ID, addr, err)
	}
	return proto.NewConn(tc), nil
}

func (c *Client) header(typ string) *proto.Header {
	h := &proto.Header{Type: typ, From: c.Self, Time: now()}
	if c.Gossip != nil {
		h.Revoked = c.Gossip()
	}
	return h
}

func (c *Client) learn(peer string, h *proto.Header) {
	if c.Learn != nil && h != nil && len(h.Revoked) > 0 {
		c.Learn(peer, h.Revoked)
	}
}

// Status asks a peer who it is and what role it plays.
func (c *Client) Status(ctx context.Context, peer config.Peer) (*proto.Status, error) {
	conn, err := c.dial(ctx, peer)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.Send(c.header(proto.TypeStatus), nil, 0); err != nil {
		return nil, err
	}
	h, _, err := conn.Expect(proto.TypeOK)
	if err != nil {
		return nil, err
	}
	c.learn(peer.ID, h)
	if h.Status == nil {
		return nil, errors.New("status reply without status")
	}
	return h.Status, nil
}

// Peers asks a peer for its whitelist.
func (c *Client) Peers(ctx context.Context, peer config.Peer) ([]proto.PeerInfo, error) {
	conn, err := c.dial(ctx, peer)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.Send(c.header(proto.TypePeers), nil, 0); err != nil {
		return nil, err
	}
	h, _, err := conn.Expect(proto.TypeOK)
	if err != nil {
		return nil, err
	}
	c.learn(peer.ID, h)
	return h.Peers, nil
}

// Deliver sends the file at path to peer. h.To names the final destination
// (empty or the peer itself for direct delivery), h.Origin the original
// sender, h.Name the relative file name, h.Hops the relays so far, and
// h.SHA256 / h.Sig / h.OriginCert the origin's proof.
// Two phases: the header is announced first so the peer can refuse before
// any bytes are sent, then the body follows.
func (c *Client) Deliver(ctx context.Context, peer config.Peer, h proto.Header, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	h.Type = proto.TypeDeliver
	h.From = c.Self
	h.Size = st.Size()
	h.Time = now()
	if h.Origin == "" {
		h.Origin = c.Self
	}
	if c.Gossip != nil {
		h.Revoked = c.Gossip()
	}
	conn, err := c.dial(ctx, peer)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.Send(&h, nil, 0); err != nil {
		return err
	}
	if rh, _, err := conn.Expect(proto.TypeOK); err != nil {
		return err
	} else {
		c.learn(peer.ID, rh)
	}
	if err := conn.Send(&proto.Header{Type: proto.TypeBody, ID: h.ID, Size: h.Size}, f, h.Size); err != nil {
		// The peer may have refused mid-stream; prefer its message.
		if _, _, rerr := conn.Expect(proto.TypeOK); rerr != nil {
			return rerr
		}
		return err
	}
	_, _, err = conn.Expect(proto.TypeOK)
	return err
}

// Pull fetches every file peer holds for us. fn must consume body fully and
// store it; an item is acknowledged (and deleted on the peer) only when fn
// returns nil. A fn error of kind reject (permanent) is reported to the
// peer so it can drop the item. Returns how many items were received.
func (c *Client) Pull(ctx context.Context, peer config.Peer, fn func(h *proto.Header, body io.Reader) error) (int, error) {
	conn, err := c.dial(ctx, peer)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if err := conn.Send(c.header(proto.TypePull), nil, 0); err != nil {
		return 0, err
	}
	count := 0
	for {
		h, body, err := conn.Recv()
		if err != nil {
			return count, err
		}
		c.learn(peer.ID, h)
		switch h.Type {
		case proto.TypeEnd:
			return count, nil
		case proto.TypeError:
			return count, &proto.RemoteError{Code: h.Code, Msg: h.Msg}
		case proto.TypeItem:
			if body.Len != h.Size {
				return count, fmt.Errorf("item %s: body length %d does not match size %d", h.Name, body.Len, h.Size)
			}
			if err := fn(h, body); err != nil {
				code := proto.CodeBusy
				if proto.IsReject(err) {
					code = proto.CodeReject
				}
				_ = conn.Send(&proto.Header{Type: proto.TypeError, ID: h.ID, Code: code, Msg: err.Error()}, nil, 0)
				if code == proto.CodeReject {
					continue // peer drops it; carry on with the next item
				}
				return count, err
			}
			if err := conn.Send(&proto.Header{Type: proto.TypeAck, ID: h.ID}, nil, 0); err != nil {
				return count, err
			}
			count++
		default:
			return count, fmt.Errorf("unexpected %s frame during pull", h.Type)
		}
	}
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
