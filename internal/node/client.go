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

	"amail/internal/config"
	"amail/internal/keys"
	"amail/internal/proto"
)

// Client makes outbound AMail calls to peers.
type Client struct {
	Self    string
	Mat     *keys.Material
	Timeout time.Duration // dial + handshake timeout
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
	tc := tls.Client(raw, c.Mat.ClientTLS(peer.ID))
	hctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("tls to %s (%s): %w", peer.ID, addr, err)
	}
	_ = tc.SetDeadline(time.Now().Add(30 * time.Minute))
	return proto.NewConn(tc), nil
}

// Status asks a peer who it is and what role it plays.
func (c *Client) Status(ctx context.Context, peer config.Peer) (*proto.Status, error) {
	conn, err := c.dial(ctx, peer)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.Send(&proto.Header{Type: proto.TypeStatus, From: c.Self, Time: now()}, nil, 0); err != nil {
		return nil, err
	}
	h, _, err := conn.Expect(proto.TypeOK)
	if err != nil {
		return nil, err
	}
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
	if err := conn.Send(&proto.Header{Type: proto.TypePeers, From: c.Self, Time: now()}, nil, 0); err != nil {
		return nil, err
	}
	h, _, err := conn.Expect(proto.TypeOK)
	if err != nil {
		return nil, err
	}
	return h.Peers, nil
}

// Deliver sends the file at path to peer. h.To names the final destination
// (empty or the peer itself for direct delivery), h.Origin the original
// sender, h.Name the relative file name, h.Hops the relays so far.
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
	conn, err := c.dial(ctx, peer)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.Send(&h, nil, 0); err != nil {
		return err
	}
	if _, _, err := conn.Expect(proto.TypeOK); err != nil {
		return err
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
// returns nil. Returns how many items were received.
func (c *Client) Pull(ctx context.Context, peer config.Peer, fn func(h *proto.Header, body io.Reader) error) (int, error) {
	conn, err := c.dial(ctx, peer)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if err := conn.Send(&proto.Header{Type: proto.TypePull, From: c.Self, Time: now()}, nil, 0); err != nil {
		return 0, err
	}
	count := 0
	for {
		h, body, err := conn.Recv()
		if err != nil {
			return count, err
		}
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
				_ = conn.Error(proto.CodeBusy, err.Error())
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
