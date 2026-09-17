package proto

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// FuzzRecv feeds arbitrary bytes to the frame reader: it must return an
// error or a frame, never panic or hang.
func FuzzRecv(f *testing.F) {
	var good bytes.Buffer
	c := NewConn(&pipeWriter{&good})
	_ = c.Send(&Header{Type: TypeStatus, From: "x"}, nil, 0)
	_ = c.Send(&Header{Type: TypeItem, Name: "a", Size: 3}, bytes.NewReader([]byte("abc")), 3)
	f.Add(good.Bytes())
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 1, 2, 3})
	f.Add([]byte{0, 0, 0, 2, '{', '}', 0, 0, 0, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		go func() {
			_, _ = a.Write(data)
			_ = a.Close()
		}()
		_ = b.SetDeadline(time.Now().Add(2 * time.Second))
		rc := NewConn(b)
		for i := 0; i < 8; i++ {
			h, body, err := rc.Recv()
			if err != nil {
				return
			}
			if h == nil || body == nil {
				t.Fatal("nil frame without error")
			}
			_, _ = io.Copy(io.Discard, body)
		}
	})
}

// pipeWriter adapts a buffer to net.Conn for building a seed corpus.
type pipeWriter struct{ b *bytes.Buffer }

func (p *pipeWriter) Read([]byte) (int, error)         { return 0, io.EOF }
func (p *pipeWriter) Write(b []byte) (int, error)      { return p.b.Write(b) }
func (p *pipeWriter) Close() error                     { return nil }
func (p *pipeWriter) LocalAddr() net.Addr              { return nil }
func (p *pipeWriter) RemoteAddr() net.Addr             { return nil }
func (p *pipeWriter) SetDeadline(time.Time) error      { return nil }
func (p *pipeWriter) SetReadDeadline(time.Time) error  { return nil }
func (p *pipeWriter) SetWriteDeadline(time.Time) error { return nil }
