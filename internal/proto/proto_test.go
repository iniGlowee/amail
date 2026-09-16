package proto

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ca, cb := NewConn(a), NewConn(b)

	payload := strings.Repeat("hello amail ", 1000)
	go func() {
		_ = ca.Send(&Header{Type: TypeDeliver, From: "x", To: "y", Name: "docs/a.txt", Size: int64(len(payload))}, strings.NewReader(payload), int64(len(payload)))
		_ = ca.Send(&Header{Type: TypeEnd}, nil, 0)
	}()
	h, body, err := cb.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if h.Type != TypeDeliver || h.Name != "docs/a.txt" || h.Proto != Version {
		t.Fatalf("bad header %+v", h)
	}
	if body.Len != int64(len(payload)) {
		t.Fatalf("body len %d", body.Len)
	}
	got, _ := io.ReadAll(body)
	if !bytes.Equal(got, []byte(payload)) {
		t.Fatal("body mismatch")
	}
	h2, body2, err := cb.Recv()
	if err != nil || h2.Type != TypeEnd || body2.Len != 0 {
		t.Fatalf("second frame: %v %+v", err, h2)
	}
}

func TestUnreadBodyIsDrained(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ca, cb := NewConn(a), NewConn(b)
	go func() {
		_ = ca.Send(&Header{Type: TypeItem, Size: 5}, strings.NewReader("12345"), 5)
		_ = ca.Send(&Header{Type: TypeEnd}, nil, 0)
	}()
	if _, _, err := cb.Recv(); err != nil {
		t.Fatal(err)
	}
	// Do not read the body; the next Recv must skip it.
	h, _, err := cb.Recv()
	if err != nil || h.Type != TypeEnd {
		t.Fatalf("got %v %+v", err, h)
	}
}

func TestExpectError(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ca, cb := NewConn(a), NewConn(b)
	go func() { _ = ca.Error(CodeReject, "nope") }()
	_, _, err := cb.Expect(TypeOK)
	if !IsReject(err) {
		t.Fatalf("expected reject, got %v", err)
	}
}
