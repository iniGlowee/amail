// Package gateway turns e-mail into AMail messages.
//
// It watches a Maildir (the kind Amazon SES + a sync script, Dovecot,
// fetchmail or getmail produce) and, for every message whose subject
// contains "#amail <node-id>", writes a message folder into a node's
// outbox so the AMail daemon delivers it:
//
//	outbox/<node-id>/<title>-<timestamp>/
//	    message.txt       plain-text body (message.html if only HTML was sent)
//	    headers.txt       From, Date, Subject, Message-ID, authentication results
//	    <attachments>     decoded with their original names
//
// It is deliberately strict about who may inject: the From address must be
// on an allow-list, the message must carry a passing SPF or DKIM result in
// the receiving server's Authentication-Results header (SES, Gmail, most
// providers add one), and an optional secret word must follow the node id.
// Mail that does not qualify is left alone; mail that does is remembered in
// a state file so it is never converted twice. The gateway never deletes or
// moves mail.
package gateway

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/iniGlowee/amail/internal/config"
	"github.com/iniGlowee/amail/internal/mailbox"
)

// Config is the gateway's JSON configuration.
type Config struct {
	Maildir        string   `json:"maildir"`         // Maildir root (has new/ cur/ tmp/)
	Outbox         string   `json:"outbox"`          // the node's outbox folder, e.g. /home/amail/AMail/outbox
	AllowedSenders []string `json:"allowed_senders"` // addresses or "@domain"
	RequireAuth    bool     `json:"require_auth"`    // need spf=pass or dkim=pass in Authentication-Results
	Tag            string   `json:"tag"`             // subject marker, default "#amail"
	Secret         string   `json:"secret,omitempty"`
	MaxMB          int      `json:"max_mb"`       // largest message to convert
	PollSeconds    int      `json:"poll_seconds"` // 0 = run once
	State          string   `json:"state"`        // file remembering processed message ids

	Outbound // AMail -> e-mail, optional (see outbound.go)
}

// Default returns a config with sensible defaults.
func Default() *Config {
	return &Config{RequireAuth: true, Tag: "#amail", MaxMB: 25, PollSeconds: 30}
}

// Load reads a gateway config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := Default()
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:] // UTF-8 BOM (Windows PowerShell 5.1 writes one)
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Tag == "" {
		c.Tag = "#amail"
	}
	if c.MaxMB <= 0 {
		c.MaxMB = 25
	}
	if c.State == "" {
		c.State = path + ".state"
	}
	return c, c.Validate()
}

// Validate checks the config. Inbound (maildir -> outbox) and outbound
// (inbox -> e-mail) are each optional, but at least one must be set up.
func (c *Config) Validate() error {
	inbound := c.Maildir != ""
	if !inbound && !c.outboundEnabled() {
		return errors.New("gateway config needs maildir (e-mail -> AMail) and/or inbox + send_command (AMail -> e-mail)")
	}
	if inbound {
		if c.Outbox == "" {
			return errors.New("gateway config needs outbox with maildir")
		}
		if len(c.AllowedSenders) == 0 {
			return errors.New("gateway config needs at least one allowed sender (an address or @domain); an open gateway would let anyone put files on your machines")
		}
		if !strings.HasPrefix(c.Tag, "#") {
			return errors.New("tag should start with '#' so ordinary subjects never match")
		}
	}
	return c.validateOutbound()
}

// Gateway converts qualifying mail.
type Gateway struct {
	cfg  *Config
	log  *log.Logger
	seen map[string]bool
	// Send overrides the send command (tests).
	Send SendFunc
}

// New builds a gateway.
func New(cfg *Config, lg *log.Logger) (*Gateway, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if lg == nil {
		lg = log.New(os.Stdout, "", log.LstdFlags)
	}
	g := &Gateway{cfg: cfg, log: lg, seen: map[string]bool{}}
	if b, err := os.ReadFile(cfg.State); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				g.seen[l] = true
			}
		}
	}
	return g, nil
}

// Result of one message.
type Result struct {
	File   string
	Status string // converted | skipped | rejected | error
	Reason string
	Dest   string
	To     string
}

// Run polls until ctx is done (or once when PollSeconds is 0).
func (g *Gateway) Run(stop <-chan struct{}) {
	if g.cfg.Maildir != "" {
		g.log.Printf("gateway: e-mail -> AMail: watching %s for %q subjects from %v -> %s", g.cfg.Maildir, g.cfg.Tag, g.cfg.AllowedSenders, g.cfg.Outbox)
	}
	if g.cfg.outboundEnabled() {
		g.log.Printf("gateway: AMail -> e-mail: watching %s for %q files from %v -> %v via %s", g.cfg.Inbox, EmailTag, g.cfg.EmailOrigins, g.cfg.EmailTo, g.cfg.SendCommand[0])
	}
	for {
		for _, r := range g.Once() {
			switch r.Status {
			case "converted":
				g.log.Printf("gateway: %s -> %s (%s)", filepath.Base(r.File), r.To, r.Dest)
			case "sent":
				g.log.Printf("gateway: e-mailed %s to %s (id %s)", r.File, r.To, r.Dest)
			case "rejected", "error":
				g.log.Printf("gateway: %s %s: %s", filepath.Base(r.File), r.Status, r.Reason)
			}
		}
		if g.cfg.PollSeconds <= 0 {
			return
		}
		select {
		case <-stop:
			return
		case <-time.After(time.Duration(g.cfg.PollSeconds) * time.Second):
		}
	}
}

// Once scans new/ and cur/ one time and returns what happened to each
// message it had not seen before.
func (g *Gateway) Once() []Result {
	var out []Result
	if g.cfg.Maildir != "" {
		out = append(out, g.inboundOnce()...)
	}
	out = append(out, g.emailOnce()...)
	return out
}

func (g *Gateway) inboundOnce() []Result {
	var out []Result
	var files []string
	for _, d := range []string{"new", "cur"} {
		entries, err := os.ReadDir(filepath.Join(g.cfg.Maildir, d))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				files = append(files, filepath.Join(g.cfg.Maildir, d, e.Name()))
			}
		}
	}
	sort.Strings(files)
	for _, f := range files {
		id := maildirID(filepath.Base(f))
		if g.seen[id] {
			continue
		}
		r := g.handle(f)
		r.File = f
		g.remember(id)
		if r.Status != "skipped" {
			out = append(out, r)
		}
	}
	return out
}

// maildirID strips the ":2,FLAGS" suffix mail readers append when a
// message moves from new/ to cur/, so a message is recognised in both.
func maildirID(name string) string {
	if i := strings.Index(name, ":"); i > 0 {
		return name[:i]
	}
	return name
}

func (g *Gateway) remember(id string) {
	g.seen[id] = true
	f, err := os.OpenFile(g.cfg.State, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(id + "\n")
	_ = f.Close()
}

var wsRe = regexp.MustCompile(`\s+`)

// Parsed is what the gateway extracted from a message.
type Parsed struct {
	From        string
	Subject     string // decoded, full
	To          string // node id from the subject
	Title       string // subject with tag / node / secret removed
	Text        string
	HTML        string
	Attachments []Attachment
	Headers     string // the headers.txt content
}

// Attachment is a decoded MIME part with a file name.
type Attachment struct {
	Name string
	Data []byte
}

func (g *Gateway) handle(path string) Result {
	st, err := os.Stat(path)
	if err != nil {
		return Result{Status: "error", Reason: err.Error()}
	}
	if st.Size() > int64(g.cfg.MaxMB)<<20 {
		return Result{Status: "rejected", Reason: fmt.Sprintf("message is %d MB, limit %d", st.Size()>>20, g.cfg.MaxMB)}
	}
	f, err := os.Open(path)
	if err != nil {
		return Result{Status: "error", Reason: err.Error()}
	}
	defer f.Close()
	p, reason, err := Parse(bufio.NewReader(f), g.cfg)
	if err != nil {
		return Result{Status: "error", Reason: err.Error()}
	}
	if reason != "" {
		if p == nil || p.To == "" {
			return Result{Status: "skipped", Reason: reason} // not for us at all
		}
		return Result{Status: "rejected", Reason: reason, To: p.To}
	}
	dest, err := g.write(p)
	if err != nil {
		return Result{Status: "error", Reason: err.Error(), To: p.To}
	}
	return Result{Status: "converted", Dest: dest, To: p.To}
}

// Parse reads a message and decides whether it qualifies. It returns the
// parsed message, a rejection reason ("" when it qualifies) and a hard
// error for unreadable input. A message whose subject has no tag returns a
// reason and a Parsed with an empty To, meaning "not addressed to AMail".
func Parse(r io.Reader, cfg *Config) (*Parsed, string, error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return nil, "", fmt.Errorf("not an e-mail: %w", err)
	}
	dec := new(mime.WordDecoder)
	subject, err := dec.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		subject = msg.Header.Get("Subject")
	}
	p := &Parsed{Subject: subject}

	// Subject: "... #amail <node> [secret] ..." (tag anywhere, case-insensitive)
	words := strings.Fields(subject)
	tagAt := -1
	for i, w := range words {
		if strings.EqualFold(w, cfg.Tag) {
			tagAt = i
			break
		}
	}
	if tagAt < 0 {
		return p, "no " + cfg.Tag + " tag in subject", nil
	}
	if tagAt+1 >= len(words) {
		return p, "tag without a node id", nil
	}
	p.To = strings.ToLower(words[tagAt+1])
	if err := config.ValidID(p.To); err != nil {
		p.To = ""
		return p, "bad node id after tag: " + err.Error(), nil
	}
	rest := append(append([]string{}, words[:tagAt]...), words[tagAt+2:]...)
	if cfg.Secret != "" {
		if tagAt+2 >= len(words) || words[tagAt+2] != cfg.Secret {
			return p, "missing or wrong secret after node id", nil
		}
		rest = append(append([]string{}, words[:tagAt]...), words[tagAt+3:]...)
	}
	p.Title = strings.TrimSpace(strings.Join(rest, " "))

	// Sender allow-list
	from := msg.Header.Get("From")
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return p, "unparseable From: " + from, nil
	}
	p.From = strings.ToLower(addr.Address)
	if !senderAllowed(p.From, cfg.AllowedSenders) {
		return p, "sender " + p.From + " is not on the allow-list", nil
	}

	// Authentication (SPF / DKIM as judged by the receiving server)
	auth := strings.ToLower(wsRe.ReplaceAllString(strings.Join(msg.Header[textproto.CanonicalMIMEHeaderKey("Authentication-Results")], " "), " "))
	spf := strings.ToLower(msg.Header.Get("Received-SPF"))
	if cfg.RequireAuth {
		ok := strings.Contains(auth, "dkim=pass") || strings.Contains(auth, "spf=pass") || strings.HasPrefix(strings.TrimSpace(spf), "pass")
		if !ok {
			return p, "no passing SPF/DKIM result on the message (Authentication-Results/Received-SPF)", nil
		}
	}
	for _, h := range []string{"X-SES-Spam-Verdict", "X-SES-Virus-Verdict"} {
		for _, raw := range msg.Header[textproto.CanonicalMIMEHeaderKey(h)] {
			if v := strings.ToUpper(strings.TrimSpace(raw)); v != "" && v != "PASS" {
				return p, h + " is " + v, nil
			}
		}
	}

	// Body and attachments
	if err := walk(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Header.Get("Content-Disposition"), msg.Body, p, 0); err != nil {
		return nil, "", err
	}
	var hb strings.Builder
	for _, h := range []string{"From", "To", "Date", "Subject", "Message-ID", "Received-SPF", "Authentication-Results", "X-SES-Spam-Verdict", "X-SES-Virus-Verdict"} {
		for _, v := range msg.Header[textproto.CanonicalMIMEHeaderKey(h)] {
			if h == "Subject" {
				v = subject
			}
			fmt.Fprintf(&hb, "%s: %s\n", h, wsRe.ReplaceAllString(v, " "))
		}
	}
	fmt.Fprintf(&hb, "Routed-To: %s\nConverted-At: %s\n", p.To, time.Now().UTC().Format(time.RFC3339))
	p.Headers = hb.String()
	return p, "", nil
}

func senderAllowed(addr string, allowed []string) bool {
	at := strings.LastIndex(addr, "@")
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "@") && at >= 0 && addr[at:] == a {
			return true
		}
		if a == addr {
			return true
		}
	}
	return false
}

// walk descends the MIME tree collecting text and attachments.
func walk(ctype, cte, cdisp string, body io.Reader, p *Parsed, depth int) error {
	if depth > 8 {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(ctype)
	if err != nil || ctype == "" {
		mediaType, params = "text/plain", map[string]string{}
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return errors.New("multipart without boundary")
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return nil // tolerate a broken tail; keep what we have
			}
			if err := walk(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part.Header.Get("Content-Disposition"), part, p, depth+1); err != nil {
				return err
			}
		}
	}
	data, err := io.ReadAll(decode(body, cte))
	if err != nil {
		return err
	}
	// Attachment? (disposition attachment, or any named non-text part)
	name := partName(cdisp, params)
	isAttachment := strings.HasPrefix(strings.ToLower(cdisp), "attachment") || (name != "" && !strings.HasPrefix(mediaType, "text/"))
	if isAttachment {
		if name == "" {
			name = "attachment-" + fmt.Sprint(len(p.Attachments)+1) + extFor(mediaType)
		}
		p.Attachments = append(p.Attachments, Attachment{Name: name, Data: data})
		return nil
	}
	switch mediaType {
	case "text/plain":
		if p.Text == "" {
			p.Text = string(data)
		} else {
			p.Text += "\n" + string(data)
		}
	case "text/html":
		if p.HTML == "" {
			p.HTML = string(data)
		}
	default:
		if len(data) > 0 {
			p.Attachments = append(p.Attachments, Attachment{Name: "part-" + fmt.Sprint(len(p.Attachments)+1) + extFor(mediaType), Data: data})
		}
	}
	return nil
}

func decode(r io.Reader, cte string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &b64Cleaner{r: r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// b64Cleaner drops whitespace so line-wrapped base64 decodes cleanly.
type b64Cleaner struct{ r io.Reader }

func (c *b64Cleaner) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	j := 0
	for i := 0; i < n; i++ {
		if b := p[i]; b != '\r' && b != '\n' && b != ' ' && b != '\t' {
			p[j] = b
			j++
		}
	}
	return j, err
}

func partName(cdisp string, ctParams map[string]string) string {
	dec := new(mime.WordDecoder)
	if cdisp != "" {
		if _, params, err := mime.ParseMediaType(cdisp); err == nil {
			if n := params["filename"]; n != "" {
				if d, err := dec.DecodeHeader(n); err == nil {
					n = d
				}
				return filepath.Base(strings.ReplaceAll(n, "\\", "/"))
			}
		}
	}
	if n := ctParams["name"]; n != "" {
		if d, err := dec.DecodeHeader(n); err == nil {
			n = d
		}
		return filepath.Base(strings.ReplaceAll(n, "\\", "/"))
	}
	return ""
}

func extFor(mediaType string) string {
	if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
		return exts[0]
	}
	switch mediaType {
	case "application/pdf":
		return ".pdf"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	}
	return ".bin"
}

var badName = regexp.MustCompile(`[\\/:*?"<>|]+`)

func safeTitle(s string) string {
	// drop a leading #tag word from the folder name; it lives in message.txt
	if strings.HasPrefix(s, "#") {
		if i := strings.IndexAny(s, " \t"); i > 0 {
			s = s[i+1:]
		} else {
			s = strings.TrimPrefix(s, "#")
		}
	}
	s = badName.ReplaceAllString(s, " ")
	s = strings.TrimSpace(wsRe.ReplaceAllString(s, " "))
	if len(s) > 60 {
		s = strings.TrimSpace(s[:60])
	}
	if s == "" {
		s = "email"
	}
	return s
}

// write lays the message out as a folder in outbox/<to>/.
func (g *Gateway) write(p *Parsed) (string, error) {
	folder := fmt.Sprintf("%s-%s", safeTitle(p.Title), time.Now().Format("20060102-150405"))
	dir := filepath.Join(g.cfg.Outbox, p.To, folder)
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(g.cfg.Outbox, p.To, fmt.Sprintf("%s (%d)", folder, i))
	}
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return "", err
	}
	put := func(name string, data []byte) error {
		clean, err := mailbox.CleanName(name)
		if err != nil {
			clean = "attachment.bin"
		}
		target := filepath.Join(dir, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o775); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o664)
	}
	body := strings.TrimSpace(p.Text)
	// A subject that starts with another #tag after the node id (for example
	// "#amail austin-pc #claude What is ...") is meant for a processor on the
	// destination: it becomes the first line of message.txt.
	if strings.HasPrefix(p.Title, "#") {
		if body != "" {
			body = p.Title + "\n\n" + body
		} else {
			body = p.Title
		}
	}
	if body != "" {
		if err := put("message.txt", []byte(body+"\n")); err != nil {
			return "", err
		}
	}
	if p.HTML != "" && body == "" {
		if err := put("message.html", []byte(p.HTML)); err != nil {
			return "", err
		}
	}
	if err := put("headers.txt", []byte(p.Headers)); err != nil {
		return "", err
	}
	used := map[string]int{}
	for _, a := range p.Attachments {
		name := a.Name
		if used[name] > 0 {
			ext := filepath.Ext(name)
			name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), used[name]+1, ext)
		}
		used[a.Name]++
		if err := put(name, a.Data); err != nil {
			return "", err
		}
	}
	return dir, nil
}
