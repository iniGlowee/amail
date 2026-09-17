// Package proto implements the AMail wire protocol: length-prefixed JSON
// headers with an optional raw body, exchanged over a mutually
// authenticated TLS connection (canonical port 4444).
//
// Frame layout (all integers big-endian):
//
//	uint32 headerLen | headerLen bytes of JSON | uint64 bodyLen | bodyLen bytes
//
// One connection carries one request. See docs/PROTOCOL.md.
package proto

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
)

// Version of the wire protocol.
const Version = 1

// MaxHeader bounds the JSON header size.
const MaxHeader = 1 << 20

// Frame types.
const (
	TypeStatus  = "status"  // request: who are you / what role are you
	TypeDeliver = "deliver" // request: announce a file (to me, or for relay)
	TypeBody    = "body"    // the file bytes that follow an accepted deliver
	TypePull    = "pull"    // request: give me everything you hold for me
	TypePeers   = "peers"   // request: your whitelist (id/host/port)
	TypeOK      = "ok"      // reply
	TypeError   = "error"   // reply
	TypeItem    = "item"    // pull reply: one held file (body follows)
	TypeAck     = "ack"     // pull: client stored the item, server may delete it
	TypeEnd     = "end"     // pull: nothing more
)

// Error codes carried in Header.Code.
const (
	CodeReject = "reject" // permanent: the sender should not retry
	CodeBusy   = "busy"   // transient: retry later
)

// Header is the JSON part of every frame.
type Header struct {
	Proto  int        `json:"proto"`
	Type   string     `json:"type"`
	From   string     `json:"from,omitempty"`   // node sending this frame
	To     string     `json:"to,omitempty"`     // final destination of a file
	Origin string     `json:"origin,omitempty"` // node that originally sent the file
	ID     string     `json:"id,omitempty"`     // message id
	Name   string     `json:"name,omitempty"`   // relative file name (slash separated)
	Size   int64      `json:"size,omitempty"`   // file size in bytes
	Hops   int        `json:"hops,omitempty"`   // relays so far
	Time   string     `json:"time,omitempty"`   // RFC3339
	Code   string     `json:"code,omitempty"`   // error code
	Msg    string     `json:"msg,omitempty"`    // human readable note
	Status *Status    `json:"status,omitempty"`
	Peers  []PeerInfo `json:"peers,omitempty"`

	// End-to-end authenticity (deliver / item): SHA-256 of the content,
	// the origin's signature over keys.Canonical(...) and the origin's
	// certificate so a receiver can verify through any number of relays.
	SHA256     string `json:"sha256,omitempty"`
	Sig        string `json:"sig,omitempty"`
	OriginCert string `json:"origin_cert,omitempty"`

	// Revoked certificate serials this node knows about; peers merge them.
	Revoked []string `json:"revoked,omitempty"`
}

// Status is the answer to a status request.
type Status struct {
	NodeID    string `json:"node_id"`
	Network   string `json:"network"`
	Role      string `json:"role"`   // server | client | searching
	Server    string `json:"server"` // id of the node currently acting as server
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Uptime    string `json:"uptime"`
	Time      string `json:"time"`
	Listening bool   `json:"listening"`
	Inbox     int    `json:"inbox"`
	Outbox    int    `json:"outbox"`
	Forward   int    `json:"forward"`
}

// PeerInfo is a whitelist entry as shared over the wire.
type PeerInfo struct {
	ID   string `json:"id"`
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
}

// RemoteError is an error frame received from the other side.
type RemoteError struct {
	Code string
	Msg  string
}

func (e *RemoteError) Error() string {
	if e.Code == "" {
		return "remote error: " + e.Msg
	}
	return "remote " + e.Code + ": " + e.Msg
}

// IsReject reports whether err is a permanent rejection by the remote node.
func IsReject(err error) bool {
	var re *RemoteError
	return errors.As(err, &re) && re.Code == CodeReject
}

// Body is the raw bytes following a header.
type Body struct {
	io.Reader
	Len int64
}

// Conn wraps a net.Conn with buffered frame I/O.
type Conn struct {
	c       net.Conn
	r       *bufio.Reader
	w       *bufio.Writer
	pending *Body
}

// NewConn wraps a connection.
func NewConn(c net.Conn) *Conn {
	return &Conn{c: c, r: bufio.NewReaderSize(c, 64<<10), w: bufio.NewWriterSize(c, 64<<10)}
}

// Close closes the underlying connection.
func (c *Conn) Close() error { return c.c.Close() }

// RemoteAddr returns the peer address.
func (c *Conn) RemoteAddr() net.Addr { return c.c.RemoteAddr() }

// Send writes one frame. body may be nil; bodyLen bytes are copied from it.
func (c *Conn) Send(h *Header, body io.Reader, bodyLen int64) error {
	h.Proto = Version
	js, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if len(js) > MaxHeader {
		return errors.New("header too large")
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(js)))
	if _, err := c.w.Write(n[:]); err != nil {
		return err
	}
	if _, err := c.w.Write(js); err != nil {
		return err
	}
	if body == nil || bodyLen < 0 {
		bodyLen = 0
	}
	var m [8]byte
	binary.BigEndian.PutUint64(m[:], uint64(bodyLen))
	if _, err := c.w.Write(m[:]); err != nil {
		return err
	}
	if bodyLen > 0 {
		if _, err := io.CopyN(c.w, body, bodyLen); err != nil {
			return fmt.Errorf("sending body: %w", err)
		}
	}
	return c.w.Flush()
}

// Error sends an error frame.
func (c *Conn) Error(code, msg string) error {
	return c.Send(&Header{Type: TypeError, Code: code, Msg: msg}, nil, 0)
}

// Recv reads one frame. The returned Body must be consumed before the next
// Recv; anything left unread is discarded automatically.
func (c *Conn) Recv() (*Header, *Body, error) {
	if c.pending != nil {
		_, _ = io.Copy(io.Discard, c.pending)
		c.pending = nil
	}
	var n [4]byte
	if _, err := io.ReadFull(c.r, n[:]); err != nil {
		return nil, nil, err
	}
	l := binary.BigEndian.Uint32(n[:])
	if l == 0 || l > MaxHeader {
		return nil, nil, fmt.Errorf("bad header length %d", l)
	}
	js := make([]byte, l)
	if _, err := io.ReadFull(c.r, js); err != nil {
		return nil, nil, err
	}
	h := &Header{}
	if err := json.Unmarshal(js, h); err != nil {
		return nil, nil, fmt.Errorf("bad header: %w", err)
	}
	if h.Proto != Version {
		return nil, nil, fmt.Errorf("unsupported protocol version %d", h.Proto)
	}
	var m [8]byte
	if _, err := io.ReadFull(c.r, m[:]); err != nil {
		return nil, nil, err
	}
	size := int64(binary.BigEndian.Uint64(m[:]))
	if size < 0 {
		return nil, nil, fmt.Errorf("bad body length")
	}
	body := &Body{Reader: io.LimitReader(c.r, size), Len: size}
	c.pending = body
	return h, body, nil
}

// Expect reads a frame and requires it to be of the given type. An error
// frame becomes a *RemoteError.
func (c *Conn) Expect(typ string) (*Header, *Body, error) {
	h, body, err := c.Recv()
	if err != nil {
		return nil, nil, err
	}
	if h.Type == TypeError {
		return nil, nil, &RemoteError{Code: h.Code, Msg: h.Msg}
	}
	if h.Type != typ {
		return nil, nil, fmt.Errorf("expected %s frame, got %s", typ, h.Type)
	}
	return h, body, nil
}
