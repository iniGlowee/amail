package gateway

// Outbound: AMail -> e-mail.
//
// A text file that arrives in the gateway node's inbox whose first line is
//
//	#email [to:someone@example.com] [style:geex] Subject text here
//
// is sent as an e-mail (style: adds an HTML part, see style.go): the rest of the file is the body, the subject is
// the rest of that first line, and, when the file is message.txt inside a
// message folder (what Compose and the inbound gateway produce), every
// other file in that folder travels as an attachment. The message is handed
// to a sendmail-style command on stdin (for example ses-send.sh, msmtp or
// /usr/sbin/sendmail), so the gateway holds no mail credentials itself.
//
// Only nodes listed in email_origins may trigger mail, and recipients are
// the configured email_to unless a "to:" override names an address on
// email_allowed_to. A receipt is written back into outbox/<origin>/ so the
// sender sees what happened.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iniGlowee/amail/internal/mailbox"
)

// EmailTag marks a file that should become an e-mail.
const EmailTag = "#email"

// SendFunc delivers a finished RFC 822 message to the recipients and
// returns an identifier (the provider's message id when known).
type SendFunc func(ctx context.Context, msg []byte, to []string) (string, error)

// Outbound is the e-mail side of the gateway config (all inside the same
// JSON file).
type Outbound struct {
	Inbox          string   `json:"inbox"`                   // the node's inbox folder to watch
	EmailFrom      string   `json:"email_from"`              // From address, e.g. info@ausa.dev
	EmailTo        []string `json:"email_to"`                // default recipients
	EmailAllowTo   []string `json:"email_allowed_to"`        // addresses a "to:" override may name (default: email_to)
	EmailOrigins   []string `json:"email_origins"`           // node ids allowed to trigger mail
	SendCommand    []string `json:"send_command"`            // sendmail-style: message on stdin, recipients as args
	EmailMaxMB     int      `json:"email_max_mb"`            // cap on body + attachments (SES raw limit is 10 MB)
	EmailReceipt   bool     `json:"email_receipt"`           // write a receipt into outbox/<origin>/
	EmailDelete    bool     `json:"email_delete_after_send"` // remove the inbox file(s) after a successful send
	EmailStableSec int      `json:"email_stable_seconds"`    // wait for files to stop changing (default 5)

	EmailStyles       map[string]string `json:"email_styles"`        // style name -> HTML template file; "" keeps the built-in (see style.go)
	EmailDefaultStyle string            `json:"email_default_style"` // style for notes without style:NAME ("" = plain text only)
}

func (c *Config) outboundEnabled() bool {
	return c.Inbox != "" && len(c.SendCommand) > 0
}

func (c *Config) validateOutbound() error {
	if !c.outboundEnabled() {
		return nil
	}
	if c.EmailFrom == "" || len(c.EmailTo) == 0 {
		return fmt.Errorf("outbound e-mail needs email_from and email_to")
	}
	if len(c.EmailOrigins) == 0 {
		return fmt.Errorf("outbound e-mail needs email_origins (node ids allowed to send mail); otherwise any member could send mail as %s", c.EmailFrom)
	}
	if c.EmailMaxMB <= 0 {
		c.EmailMaxMB = 7
	}
	if c.EmailStableSec <= 0 {
		c.EmailStableSec = 5
	}
	if len(c.EmailAllowTo) == 0 {
		c.EmailAllowTo = c.EmailTo
	}
	if s := c.EmailDefaultStyle; s != "" && s != BuiltinStyle {
		if _, ok := c.EmailStyles[s]; !ok {
			return fmt.Errorf("email_default_style %q is neither %s nor listed under email_styles", s, BuiltinStyle)
		}
	}
	return nil
}

// Outgoing is a parsed #email file.
type Outgoing struct {
	Path        string
	Origin      string
	To          []string
	Subject     string
	Style       string // HTML style name from style:NAME, "" = config default
	Body        string
	Attachments []string // paths
}

// emailOnce scans the inbox for #email files and sends them.
func (g *Gateway) emailOnce() []Result {
	if !g.cfg.outboundEnabled() {
		return nil
	}
	var out []Result
	origins, err := os.ReadDir(g.cfg.Inbox)
	if err != nil {
		return nil
	}
	stable := time.Duration(g.cfg.EmailStableSec) * time.Second
	for _, o := range origins {
		if !o.IsDir() || strings.HasPrefix(o.Name(), ".") {
			continue
		}
		origin := o.Name()
		base := filepath.Join(g.cfg.Inbox, origin)
		var files []string
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || mailbox.IsTemp(d.Name()) {
				return nil
			}
			files = append(files, p)
			return nil
		})
		sort.Strings(files)
		for _, p := range files {
			key := "email:" + p
			if g.seen[key] {
				continue
			}
			st, err := os.Stat(p)
			if err != nil || time.Since(st.ModTime()) < stable {
				continue
			}
			og, ok := g.parseOutgoing(p, origin, st.Size())
			if !ok {
				g.remember(key) // not an #email file; never look again
				continue
			}
			r := Result{File: p, To: strings.Join(og.To, ","), Status: "sent"}
			if !contains(g.cfg.EmailOrigins, origin) {
				r.Status, r.Reason = "rejected", "node "+origin+" is not allowed to send e-mail"
				g.remember(key)
				g.receipt(og, r)
				out = append(out, r)
				continue
			}
			msg, err := g.build(og)
			if err != nil {
				r.Status, r.Reason = "rejected", err.Error()
				g.remember(key)
				g.receipt(og, r)
				out = append(out, r)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			id, err := g.send(ctx, msg, og.To)
			cancel()
			if err != nil {
				// transient (command failed): keep the file, log, retry next poll
				r.Status, r.Reason = "error", err.Error()
				out = append(out, r)
				continue
			}
			r.Dest = id
			g.remember(key)
			g.receipt(og, r)
			if g.cfg.EmailDelete {
				_ = os.Remove(og.Path)
				for _, a := range og.Attachments {
					_ = os.Remove(a)
				}
				_ = os.Remove(filepath.Dir(og.Path)) // only succeeds when empty
			}
			out = append(out, r)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), s) {
			return true
		}
	}
	return false
}

// parseOutgoing reads the file and reports whether it is an #email file.
func (g *Gateway) parseOutgoing(path, origin string, size int64) (*Outgoing, bool) {
	if size > 4<<20 { // bodies are text; big files are never #email notes
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	first := ""
	for sc.Scan() {
		first = strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if first != "" {
			break
		}
	}
	if !strings.HasPrefix(strings.ToLower(first), EmailTag) {
		return nil, false
	}
	rest := strings.TrimSpace(first[len(EmailTag):])
	if rest != "" && !strings.HasPrefix(first[len(EmailTag):], " ") && !strings.HasPrefix(first[len(EmailTag):], "\t") {
		return nil, false // "#emailing" is not the tag
	}
	og := &Outgoing{Path: path, Origin: origin, To: append([]string(nil), g.cfg.EmailTo...)}
	words := strings.Fields(rest)
	var subj []string
	for _, w := range words {
		lw := strings.ToLower(w)
		switch {
		case len(subj) == 0 && strings.HasPrefix(lw, "to:"):
			og.To = strings.Split(w[3:], ",")
		case len(subj) == 0 && strings.HasPrefix(lw, "style:"):
			og.Style = lw[6:]
		default:
			subj = append(subj, w)
		}
	}
	og.Subject = strings.TrimSpace(strings.Join(subj, " "))
	if og.Subject == "" {
		og.Subject = "(no subject) from AMail node " + origin
	}
	var body strings.Builder
	for sc.Scan() {
		body.WriteString(sc.Text())
		body.WriteString("\n")
	}
	og.Body = strings.TrimSpace(body.String())
	// attachments: siblings of message.txt inside a message folder
	if strings.EqualFold(filepath.Base(path), "message.txt") {
		dir := filepath.Dir(path)
		if filepath.Clean(dir) != filepath.Clean(filepath.Join(g.cfg.Inbox, origin)) {
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || p == path || mailbox.IsTemp(d.Name()) {
					return nil
				}
				if strings.EqualFold(d.Name(), "headers.txt") {
					return nil
				}
				og.Attachments = append(og.Attachments, p)
				return nil
			})
			sort.Strings(og.Attachments)
		}
	}
	return og, true
}

// build assembles the RFC 822 message.
func (g *Gateway) build(og *Outgoing) ([]byte, error) {
	for i, a := range og.To {
		a = strings.TrimSpace(a)
		if a == "" {
			return nil, fmt.Errorf("empty recipient")
		}
		if _, err := mail.ParseAddress(a); err != nil {
			return nil, fmt.Errorf("bad recipient %q", a)
		}
		if !contains(g.cfg.EmailAllowTo, a) {
			return nil, fmt.Errorf("recipient %s is not on email_allowed_to", a)
		}
		og.To[i] = a
	}
	var total int64
	for _, a := range og.Attachments {
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		total += st.Size()
	}
	limit := int64(g.cfg.EmailMaxMB) << 20
	if total+int64(len(og.Body)) > limit {
		return nil, fmt.Errorf("message is %d MB, e-mail limit is %d MB (send big files through AMail instead)", (total+int64(len(og.Body)))>>20, g.cfg.EmailMaxMB)
	}
	style := og.Style
	if style == "" {
		style = g.cfg.EmailDefaultStyle
	}
	htmlBody := ""
	if style != "" {
		h, err := g.renderStyle(style, og)
		if err != nil {
			return nil, err
		}
		htmlBody = h
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", g.cfg.EmailFrom)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(og.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", og.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <amail-%d@%s>\r\n", time.Now().UnixNano(), domainOf(g.cfg.EmailFrom))
	fmt.Fprintf(&b, "X-AMail-Origin: %s\r\n", og.Origin)
	b.WriteString("MIME-Version: 1.0\r\n")
	bodyCT, bodyBytes := bodyPart(og.Body, htmlBody)
	if len(og.Attachments) == 0 {
		b.WriteString("Content-Type: " + bodyCT + "\r\n")
		if htmlBody == "" {
			b.WriteString("Content-Transfer-Encoding: base64\r\n")
		}
		b.WriteString("\r\n")
		b.Write(bodyBytes)
		return b.Bytes(), nil
	}
	mw := multipart.NewWriter(&b)
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mw.Boundary())
	th := textproto.MIMEHeader{}
	th.Set("Content-Type", bodyCT)
	if htmlBody == "" {
		th.Set("Content-Transfer-Encoding", "base64")
	}
	pw, err := mw.CreatePart(th)
	if err != nil {
		return nil, err
	}
	pw.Write(bodyBytes)
	for _, a := range og.Attachments {
		data, err := os.ReadFile(a)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(a)
		ah := textproto.MIMEHeader{}
		ah.Set("Content-Type", contentTypeFor(name))
		ah.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", mime.QEncoding.Encode("utf-8", name)))
		ah.Set("Content-Transfer-Encoding", "base64")
		aw, err := mw.CreatePart(ah)
		if err != nil {
			return nil, err
		}
		aw.Write([]byte(wrap76(base64.StdEncoding.EncodeToString(data))))
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// bodyPart returns the content type and encoded bytes of the message body:
// base64 text when there is no HTML, otherwise a multipart/alternative with
// the text first and the HTML second (clients show the last part they can).
func bodyPart(text, htmlBody string) (string, []byte) {
	enc := func(s string) []byte { return []byte(wrap76(base64.StdEncoding.EncodeToString([]byte(s + "\n")))) }
	if htmlBody == "" {
		return "text/plain; charset=utf-8", enc(text)
	}
	var buf bytes.Buffer
	aw := multipart.NewWriter(&buf)
	for _, p := range []struct{ ct, body string }{{"text/plain; charset=utf-8", text}, {"text/html; charset=utf-8", htmlBody}} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", p.ct)
		h.Set("Content-Transfer-Encoding", "base64")
		w, _ := aw.CreatePart(h)
		w.Write(enc(p.body))
	}
	aw.Close()
	return fmt.Sprintf("multipart/alternative; boundary=%q", aw.Boundary()), buf.Bytes()
}

func contentTypeFor(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	return "application/octet-stream"
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return addr[i+1:]
	}
	return "amail.local"
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteString("\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	b.WriteString("\r\n")
	return b.String()
}

// send runs the configured command (or the test hook).
func (g *Gateway) send(ctx context.Context, msg []byte, to []string) (string, error) {
	if g.Send != nil {
		return g.Send(ctx, msg, to)
	}
	args := append(append([]string{}, g.cfg.SendCommand[1:]...), to...)
	cmd := exec.CommandContext(ctx, g.cfg.SendCommand[0], args...)
	hideWindow(cmd)
	cmd.Stdin = bytes.NewReader(msg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v: %s", filepath.Base(g.cfg.SendCommand[0]), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// receipt tells the origin node what happened, via its own inbox.
func (g *Gateway) receipt(og *Outgoing, r Result) {
	if !g.cfg.EmailReceipt || g.cfg.Outbox == "" {
		return
	}
	dir := filepath.Join(g.cfg.Outbox, og.Origin)
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return
	}
	name := fmt.Sprintf("email-receipt-%s.txt", time.Now().Format("20060102-150405"))
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("email-receipt-%s (%d).txt", time.Now().Format("20060102-150405"), i)
	}
	var text string
	switch r.Status {
	case "sent":
		text = fmt.Sprintf("E-mail sent\nTo: %s\nSubject: %s\nFrom: %s\nProvider id: %s\nSource: %s\nWhen: %s\n", r.To, og.Subject, g.cfg.EmailFrom, r.Dest, og.Path, time.Now().Format(time.RFC3339))
	default:
		text = fmt.Sprintf("E-mail NOT sent\nSubject: %s\nReason: %s\nSource: %s\nWhen: %s\n", og.Subject, r.Reason, og.Path, time.Now().Format(time.RFC3339))
	}
	_ = os.WriteFile(filepath.Join(dir, name), []byte(text), 0o664)
}
