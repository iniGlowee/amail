package ui

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amail/internal/config"
	"amail/internal/mailbox"
)

func newServer(t *testing.T) (*Server, *config.Config, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default(home, "alpha")
	cfg.Mailbox = filepath.Join(home, "mb")
	cfg.Whitelist = []config.Peer{{ID: "alpha"}, {ID: "beta", Note: "friend"}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	s := &Server{Home: home, Version: "test"}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, cfg, ts
}

func call(t *testing.T, ts *httptest.Server, method, path string, body io.Reader, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, body)
	req.Header.Set("X-AMail-UI", "1")
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, b
}

func TestGuards(t *testing.T) {
	_, _, ts := newServer(t)
	// missing header
	req, _ := http.NewRequest("GET", ts.URL+"/api/mail", nil)
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 403 {
		t.Fatalf("no header: %d", res.StatusCode)
	}
	// foreign origin
	res, _ = call(t, ts, "GET", "/api/mail", nil, map[string]string{"Origin": "http://evil.example"})
	if res.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", res.StatusCode)
	}
	// foreign host header
	res, _ = call(t, ts, "GET", "/api/mail", nil, map[string]string{"Host": "evil.example"})
	if res.StatusCode != 403 {
		t.Fatalf("foreign host: %d", res.StatusCode)
	}
	// the page itself is served
	res, b := call(t, ts, "GET", "/", nil, nil)
	if res.StatusCode != 200 || !bytes.Contains(b, []byte("AMail")) {
		t.Fatalf("index: %d", res.StatusCode)
	}
}

func TestMailFlow(t *testing.T) {
	_, cfg, ts := newServer(t)
	mb, _ := mailbox.Open(cfg.Mailbox)
	// send a note
	res, b := call(t, ts, "POST", "/api/send", strings.NewReader(`{"to":"beta","text":"hello there","name":"hi"}`), map[string]string{"Content-Type": "application/json"})
	if res.StatusCode != 200 {
		t.Fatalf("send note: %d %s", res.StatusCode, b)
	}
	// upload two files
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("to", "beta")
	for _, n := range []string{"a.txt", "../evil.txt"} {
		fw, _ := mw.CreateFormFile("files", n)
		_, _ = fw.Write([]byte("content of " + n))
	}
	mw.Close()
	res, b = call(t, ts, "POST", "/api/send", &buf, map[string]string{"Content-Type": mw.FormDataContentType()})
	if res.StatusCode != 200 {
		t.Fatalf("upload: %d %s", res.StatusCode, b)
	}
	// list outbox
	res, b = call(t, ts, "GET", "/api/mail?folder=outbox", nil, nil)
	var items []Mail
	_ = json.Unmarshal(b, &items)
	if res.StatusCode != 200 || len(items) != 3 {
		t.Fatalf("outbox list: %d %s", res.StatusCode, b)
	}
	names := map[string]bool{}
	for _, it := range items {
		names[it.Name] = true
		if it.Peer != "beta" {
			t.Fatalf("peer %q", it.Peer)
		}
	}
	if !names["hi.txt"] || !names["a.txt"] || !names["evil.txt"] {
		t.Fatalf("names: %v", names)
	}
	if _, err := os.Stat(filepath.Join(cfg.Mailbox, "evil.txt")); err == nil {
		t.Fatal("path traversal in upload name")
	}
	// read a file inline
	res, b = call(t, ts, "GET", "/api/file?folder=outbox&peer=beta&name=hi.txt&inline=1", nil, nil)
	if res.StatusCode != 200 || string(b) != "hello there" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("file: %d %q %s", res.StatusCode, b, res.Header.Get("Content-Type"))
	}
	// traversal on read
	res, _ = call(t, ts, "GET", "/api/file?folder=outbox&peer=beta&name=../../config.json", nil, nil)
	if res.StatusCode == 200 {
		t.Fatal("traversal on read")
	}
	res, _ = call(t, ts, "GET", "/api/file?folder=keys&peer=beta&name=x", nil, nil)
	if res.StatusCode == 200 {
		t.Fatal("unknown folder served")
	}
	// delete
	res, b = call(t, ts, "POST", "/api/delete", strings.NewReader(`{"folder":"outbox","peer":"beta","name":"hi.txt"}`), map[string]string{"Content-Type": "application/json"})
	if res.StatusCode != 200 {
		t.Fatalf("delete: %d %s", res.StatusCode, b)
	}
	if _, err := os.Stat(filepath.Join(mb.Dir(mailbox.DirOutbox), "beta", "hi.txt")); err == nil {
		t.Fatal("not deleted")
	}
	// inbox listing with a received file
	_, _ = mb.Receive("beta", "docs/r.md", strings.NewReader("# hi"), 4)
	res, b = call(t, ts, "GET", "/api/mail?folder=inbox", nil, nil)
	_ = json.Unmarshal(b, &items)
	if len(items) != 1 || items[0].Name != "docs/r.md" || items[0].Kind != "text" {
		t.Fatalf("inbox: %s", b)
	}
}

func TestSettings(t *testing.T) {
	s, cfg, ts := newServer(t)
	restarted := 0
	s.Restart = func() { restarted++ }
	res, b := call(t, ts, "GET", "/api/settings", nil, nil)
	if res.StatusCode != 200 || !bytes.Contains(b, []byte(`"node_id":"alpha"`)) {
		t.Fatalf("get: %d %s", res.StatusCode, b)
	}
	var c config.Config
	_ = json.Unmarshal(b, &c)
	c.NodeID = "hacker" // must be ignored
	c.PollSeconds = 7
	c.Listen = "off"
	c.Whitelist = append(c.Whitelist, config.Peer{ID: "gamma", Host: "10.1.1.1"})
	c.Blacklist = []config.Block{{Host: "10.0.0.0/8", Reason: "lab"}}
	js, _ := json.Marshal(c)
	res, b = call(t, ts, "POST", "/api/settings", bytes.NewReader(js), map[string]string{"Content-Type": "application/json"})
	if res.StatusCode != 200 || restarted != 1 {
		t.Fatalf("post: %d %s restarted=%d", res.StatusCode, b, restarted)
	}
	got, err := config.Load(cfg.Home)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != "alpha" || got.PollSeconds != 7 || !got.ClientOnly() || len(got.Whitelist) != 3 || len(got.Blacklist) != 1 {
		t.Fatalf("saved: %+v", got)
	}
	// invalid config is refused
	c.Whitelist = append(c.Whitelist, config.Peer{ID: "Bad Id"})
	js, _ = json.Marshal(c)
	res, _ = call(t, ts, "POST", "/api/settings", bytes.NewReader(js), map[string]string{"Content-Type": "application/json"})
	if res.StatusCode != 400 {
		t.Fatalf("invalid accepted: %d", res.StatusCode)
	}
	// overview works without a key or daemon
	res, b = call(t, ts, "GET", "/api/overview", nil, nil)
	if res.StatusCode != 200 || !bytes.Contains(b, []byte(`"key_error"`)) {
		t.Fatalf("overview: %d %s", res.StatusCode, b)
	}
}
