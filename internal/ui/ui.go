// Package ui is the local web interface: a small HTTP server, loopback only,
// that reads and writes the same mailbox folders the daemon uses and edits
// the node's config.json. It is embedded in the amail binary
// (`amail ui`, or served by `amail run` when ui_listen is set).
//
// Safety: it binds to loopback unless told otherwise, checks the Host and
// Origin headers so a web page in the same browser cannot drive it, and
// every /api call (except file downloads) must carry the X-AMail-UI header,
// which cross-site forms cannot set.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"amail/internal/config"
	"amail/internal/keys"
	"amail/internal/mailbox"
	"amail/internal/node"
	"amail/internal/proto"
)

//go:embed static
var static embed.FS

// StatusSource is what the UI asks for the local node's live status.
// The daemon implements it in-process; the standalone UI probes over TLS.
type StatusSource interface {
	Status() *proto.Status
}

// Server serves the UI for one node home.
type Server struct {
	Home    string
	Version string
	// Restart, when set, is called after settings are saved so the daemon
	// can reload. Nil in the standalone `amail ui`.
	Restart func()

	mu    sync.Mutex
	local StatusSource
	addr  string
}

// SetLocal registers the in-process node (may be called again after a restart).
func (s *Server) SetLocal(src StatusSource) {
	s.mu.Lock()
	s.local = src
	s.mu.Unlock()
}

func (s *Server) localStatus() StatusSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.local
}

// Handler builds the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	fileServer := http.FileServer(http.FS(sub))
	index, _ := static.ReadFile("static/index.html")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/overview", s.apiOverview)
	mux.HandleFunc("/api/mail", s.apiMail)
	mux.HandleFunc("/api/file", s.apiFile)
	mux.HandleFunc("/api/delete", s.apiDelete)
	mux.HandleFunc("/api/send", s.apiSend)
	mux.HandleFunc("/api/settings", s.apiSettings)
	mux.HandleFunc("/api/log", s.apiLog)
	mux.HandleFunc("/api/history", s.apiHistory)
	mux.HandleFunc("/api/restart", s.apiRestart)
	return s.guard(mux)
}

// guard enforces the same-origin rules described in the package comment.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !s.hostAllowed(host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/file" && r.Header.Get("X-AMail-UI") == "" {
			http.Error(w, "missing X-AMail-UI header", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(host string) bool {
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]" {
		return true
	}
	s.mu.Lock()
	addr := s.addr
	s.mu.Unlock()
	if addr == "" {
		return false
	}
	bh, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return bh == host
}

// ListenAndServe runs the UI until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.addr = ln.Addr().String()
	s.mu.Unlock()
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Addr returns the bound address once listening.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// --- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) load() (*config.Config, *mailbox.Mailbox, error) {
	cfg, err := config.Load(s.Home)
	if err != nil {
		return nil, nil, err
	}
	mb, err := mailbox.Open(cfg.Mailbox)
	if err != nil {
		return nil, nil, err
	}
	return cfg, mb, nil
}

var folders = map[string]bool{
	mailbox.DirInbox: true, mailbox.DirOutbox: true, mailbox.DirSent: true,
	mailbox.DirForward: true, mailbox.DirFailed: true,
}

// resolve turns (folder, peer, name) into a path inside the mailbox, or errors.
func resolve(mb *mailbox.Mailbox, folder, peer, name string) (string, error) {
	if !folders[folder] {
		return "", errors.New("unknown folder")
	}
	if err := config.ValidID(peer); err != nil {
		return "", err
	}
	clean, err := mailbox.CleanName(name)
	if err != nil {
		return "", err
	}
	base := filepath.Join(mb.Dir(folder), peer)
	p := filepath.Join(base, filepath.FromSlash(clean))
	rel, err := filepath.Rel(base, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("path escapes the mailbox")
	}
	return p, nil
}

// Mail is one file in a folder as shown in the UI.
type Mail struct {
	Folder string `json:"folder"`
	Peer   string `json:"peer"` // sender (inbox), recipient (outbox/sent/failed/forward)
	Origin string `json:"origin,omitempty"`
	Name   string `json:"name"` // relative, slash separated
	Size   int64  `json:"size"`
	Time   string `json:"time"`
	Kind   string `json:"kind"` // text | image | pdf | other
	Error  string `json:"error,omitempty"`
	Hops   int    `json:"hops,omitempty"`
}

// mimeTypes covers what browsers can play or show inline; anything else is
// offered as a download. Go's mime package is consulted after this table.
var mimeTypes = map[string]string{
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".flac": "audio/flac",
	".m4a": "audio/mp4", ".aac": "audio/aac", ".opus": "audio/opus", ".weba": "audio/webm",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime", ".ogv": "video/ogg",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".svg": "image/svg+xml", ".bmp": "image/bmp", ".avif": "image/avif",
	".pdf": "application/pdf", ".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8",
}

func kindOf(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".txt", ".md", ".log", ".json", ".csv", ".xml", ".yml", ".yaml", ".ini", ".conf", ".sh", ".ps1", ".php", ".go", ".js", ".css", ".html", ".htm", ".sql", ".py", ".toml", ".env":
		return "text"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".avif":
		return "image"
	case ".pdf":
		return "pdf"
	}
	if t := mimeTypes[ext]; strings.HasPrefix(t, "audio/") {
		return "audio"
	} else if strings.HasPrefix(t, "video/") {
		return "video"
	}
	return "other"
}

func contentType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if t, ok := mimeTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

func listFolder(mb *mailbox.Mailbox, folder string) ([]Mail, error) {
	if !folders[folder] {
		return nil, errors.New("unknown folder")
	}
	var out []Mail
	peers, err := os.ReadDir(mb.Dir(folder))
	if err != nil {
		return nil, err
	}
	for _, pe := range peers {
		if !pe.IsDir() || strings.HasPrefix(pe.Name(), ".") {
			continue
		}
		peer := pe.Name()
		base := filepath.Join(mb.Dir(folder), peer)
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			n := d.Name()
			if mailbox.IsTemp(n) || strings.HasSuffix(n, ".error.txt") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(base, p)
			m := Mail{Folder: folder, Peer: peer, Name: filepath.ToSlash(rel), Size: info.Size(), Time: info.ModTime().Format(time.RFC3339), Kind: kindOf(n)}
			if folder == mailbox.DirForward {
				// forward/<to>/<origin>/<rel>
				if i := strings.IndexByte(m.Name, '/'); i > 0 {
					m.Origin, m.Name = m.Name[:i], m.Name[i+1:]
					m.Name = m.Origin + "/" + m.Name
				}
				if js, err := os.ReadFile(p + mailbox.MetaExt); err == nil {
					var meta mailbox.Meta
					if json.Unmarshal(js, &meta) == nil {
						m.Hops = meta.Hops
					}
				}
			}
			if folder == mailbox.DirFailed {
				if b, err := os.ReadFile(p + ".error.txt"); err == nil {
					lines := strings.Split(strings.TrimSpace(string(b)), "\n")
					m.Error = lines[len(lines)-1]
				}
			}
			out = append(out, m)
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out, nil
}

// --- API ------------------------------------------------------------------

type peerView struct {
	config.Peer
	Status *proto.Status `json:"status,omitempty"`
	Error  string        `json:"error,omitempty"`
}

func (s *Server) apiOverview(w http.ResponseWriter, r *http.Request) {
	cfg, mb, err := s.load()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	in, out, fw := mb.Counts()
	res := map[string]any{
		"node_id": cfg.NodeID, "home": s.Home, "config": config.Path(s.Home), "mailbox": mb.Root,
		"version": s.Version, "listen": cfg.Listen, "client_only": cfg.ClientOnly(),
		"counts":   map[string]int{"inbox": in, "outbox": out, "forward": fw},
		"usage_mb": mb.Usage() >> 20, "max_mailbox_mb": cfg.MaxMailboxMB,
		"in_process": s.Restart != nil,
	}
	if free, err := mailbox.FreeBytes(mb.Root); err == nil {
		res["free_mb"] = free >> 20
	}
	mat, kerr := keys.Load(s.Home)
	if kerr != nil {
		res["key_error"] = kerr.Error()
	} else {
		res["network"] = mat.Network
		res["key_expires"] = mat.NotAfter.Format("2006-01-02")
	}
	if src := s.localStatus(); src != nil {
		res["local"] = src.Status()
	} else if mat != nil && !cfg.ClientOnly() {
		cli := node.NewClient(mat)
		cli.Timeout = 2 * time.Second
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if st, err := cli.Status(ctx, config.Peer{ID: cfg.NodeID, Host: "127.0.0.1", Port: cfg.ListenPort()}); err == nil {
			res["local"] = st
		} else {
			res["local_error"] = "node not running on this machine (" + err.Error() + ")"
		}
	} else if cfg.ClientOnly() {
		res["local_error"] = "client-only node (listen: off): no local status port"
	}
	// peers, probed concurrently
	views := make([]peerView, len(cfg.Whitelist))
	var wg sync.WaitGroup
	for i, p := range cfg.Whitelist {
		views[i].Peer = p
		if p.ID == cfg.NodeID {
			views[i].Error = "this node"
			continue
		}
		if p.Host == "" {
			views[i].Error = "no host (reachable only via the server)"
			continue
		}
		if mat == nil {
			views[i].Error = "no key"
			continue
		}
		wg.Add(1)
		go func(i int, p config.Peer) {
			defer wg.Done()
			cli := node.NewClient(mat)
			cli.Timeout = 3 * time.Second
			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			defer cancel()
			st, err := cli.Status(ctx, p)
			if err != nil {
				views[i].Error = shortErr(err)
				return
			}
			views[i].Status = st
		}(i, p)
	}
	wg.Wait()
	res["peers"] = views
	writeJSON(w, res)
}

func shortErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "refused"):
		return "not running / port closed"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
		return "no answer (firewall or offline)"
	case strings.Contains(s, "no such host"):
		return "unknown host"
	}
	return s
}

func (s *Server) apiMail(w http.ResponseWriter, r *http.Request) {
	_, mb, err := s.load()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = mailbox.DirInbox
	}
	items, err := listFolder(mb, folder)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if items == nil {
		items = []Mail{}
	}
	writeJSON(w, items)
}

const maxInline = 2 << 20

func (s *Server) apiFile(w http.ResponseWriter, r *http.Request) {
	_, mb, err := s.load()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	q := r.URL.Query()
	p, err := resolve(mb, q.Get("folder"), q.Get("peer"), q.Get("name"))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	f, err := os.Open(p)
	if err != nil {
		fail(w, 404, "no such file")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		fail(w, 404, "no such file")
		return
	}
	base := filepath.Base(p)
	kind := kindOf(base)
	// Text is shown inline only when small; media streams inline at any size
	// (ServeContent handles range requests, so audio and video can seek).
	inline := q.Get("inline") == "1" && kind != "other" && (kind != "text" || st.Size() <= maxInline)
	if inline {
		ctype := contentType(base)
		if kind == "text" {
			ctype = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", "inline; filename=\""+base+"\"")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+base+"\"")
	}
	http.ServeContent(w, r, base, st.ModTime(), f)
}

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST only")
		return
	}
	_, mb, err := s.load()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	var req struct {
		Folder string `json:"folder"`
		Peer   string `json:"peer"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad json")
		return
	}
	p, err := resolve(mb, req.Folder, req.Peer, req.Name)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := mb.Delete(p); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) apiSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST only")
		return
	}
	cfg, mb, err := s.load()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	var queued []string
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		// Fields, in order: to, optional prefix (message folder), then for each
		// file an optional "path" (relative name, keeps dropped folders intact)
		// followed by the "files" part itself.
		to, prefix, nextPath := "", "", ""
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				fail(w, 400, err.Error())
				return
			}
			switch part.FormName() {
			case "to":
				b, _ := io.ReadAll(io.LimitReader(part, 256))
				to = strings.TrimSpace(string(b))
			case "prefix":
				b, _ := io.ReadAll(io.LimitReader(part, 1024))
				prefix = strings.Trim(strings.TrimSpace(string(b)), "/")
			case "path":
				b, _ := io.ReadAll(io.LimitReader(part, 2048))
				nextPath = strings.TrimSpace(string(b))
			case "files":
				if to == "" {
					fail(w, 400, "recipient (to) must come before files")
					return
				}
				if part.FileName() == "" {
					continue
				}
				name := nextPath
				nextPath = ""
				if name == "" {
					name = filepath.Base(part.FileName())
				}
				if prefix != "" {
					name = prefix + "/" + name
				}
				path, err := mb.Enqueue(to, name, io.LimitReader(part, cfg.MaxBytes()+1))
				if err != nil {
					fail(w, 400, err.Error())
					return
				}
				queued = append(queued, path)
			}
		}
	} else {
		var req struct {
			To   string `json:"to"`
			Name string `json:"name"`
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
			fail(w, 400, "bad json")
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			fail(w, 400, "empty note")
			return
		}
		if req.Name == "" {
			req.Name = "note-" + time.Now().Format("20060102-150405") + ".txt"
		}
		if !strings.Contains(req.Name, ".") {
			req.Name += ".txt"
		}
		path, err := mb.Enqueue(req.To, req.Name, strings.NewReader(req.Text))
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		queued = append(queued, path)
	}
	if len(queued) == 0 {
		fail(w, 400, "nothing to send")
		return
	}
	names := make([]string, len(queued))
	for i, q := range queued {
		if rel, err := filepath.Rel(mb.Dir(mailbox.DirOutbox), q); err == nil {
			names[i] = filepath.ToSlash(rel)
		} else {
			names[i] = filepath.Base(q)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "queued": names})
}

func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := config.Load(s.Home)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		writeJSON(w, cfg)
	case http.MethodPost:
		cur, err := config.Load(s.Home)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		nc := &config.Config{}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(nc); err != nil {
			fail(w, 400, "bad json: "+err.Error())
			return
		}
		nc.Home = s.Home
		nc.NodeID = cur.NodeID // identity comes from the key, never from the form
		if nc.Mailbox == "" {
			nc.Mailbox = cur.Mailbox
		}
		if err := nc.Validate(); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if _, err := mailbox.Open(nc.Mailbox); err != nil {
			fail(w, 400, "mailbox: "+err.Error())
			return
		}
		if err := nc.Save(); err != nil {
			fail(w, 500, err.Error())
			return
		}
		restarted := false
		if s.Restart != nil {
			s.Restart()
			restarted = true
		}
		writeJSON(w, map[string]any{"ok": true, "restarted": restarted})
	default:
		fail(w, 405, "GET or POST")
	}
}

func tailLines(path string, n int) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (s *Server) apiLog(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > 2000 {
		n = 200
	}
	writeJSON(w, tailLines(filepath.Join(s.Home, "amail.log"), n))
}

func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > 2000 {
		n = 200
	}
	lines := tailLines(filepath.Join(s.Home, "state", "history.jsonl"), n)
	out := make([]map[string]any, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		var m map[string]any
		if json.Unmarshal([]byte(lines[i]), &m) == nil {
			out = append(out, m)
		}
	}
	writeJSON(w, out)
}

func (s *Server) apiRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST only")
		return
	}
	if s.Restart == nil {
		fail(w, 400, "the node is not running inside this UI process; restart the service instead")
		return
	}
	s.Restart()
	writeJSON(w, map[string]any{"ok": true})
}

// OpenBrowser tries to open url in the default browser; failures are ignored.
func OpenBrowser(url string) {
	_ = openBrowser(url)
}

// Off reports whether a ui_listen value disables the UI.
func Off(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "" || v == "off" || v == "none"
}

var _ = fmt.Sprintf
