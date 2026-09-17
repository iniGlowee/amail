package node

import (
	"net"
	"time"
)

// idleConn closes a connection that makes no progress for idle: every Read
// or Write pushes the deadline forward, so a large transfer is fine but a
// peer that stalls (deliberately or not) cannot hold a slot for long.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func withIdle(c net.Conn, idle time.Duration) net.Conn {
	_ = c.SetDeadline(time.Now().Add(idle))
	return &idleConn{Conn: c, idle: idle}
}

func (c *idleConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err == nil {
		_ = c.Conn.SetDeadline(time.Now().Add(c.idle))
	}
	return n, err
}

func (c *idleConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if err == nil {
		_ = c.Conn.SetDeadline(time.Now().Add(c.idle))
	}
	return n, err
}

// SetDeadline is kept so TLS handshake contexts can still apply their own
// shorter deadline; the idle wrapper re-extends it on progress.
func (c *idleConn) SetDeadline(t time.Time) error { return c.Conn.SetDeadline(t) }
