// Package processor runs configured commands on tagged notes.
//
// It watches a node's inbox for text files whose first line starts with a
// handler tag, for example
//
//	#claude What is the capital of France?
//
// feeds the rest of that line plus the body to the handler's command on
// stdin, and writes the command's stdout as a new note into the outbox for
// another node, with a first line of the handler's choosing (typically
// "#email ..." so the e-mail gateway on that node mails it out). That is
// how a question sent by e-mail can be answered by a program on a PC and
// come back by e-mail, with AMail carrying it both ways.
//
// Safety: only files from nodes on allowed_origins are considered; the
// command comes from the config, never from the message; input and output
// sizes are capped; every command has a timeout; a note is processed once.
package processor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iniGlowee/amail/internal/config"
	"github.com/iniGlowee/amail/internal/mailbox"
)

// Handler maps a tag to a command.
type Handler struct {
	Tag            string   `json:"tag"`              // e.g. "#claude"
	Command        []string `json:"command"`          // runs with the prompt on stdin; stdout is the reply
	ReplyTo        string   `json:"reply_to"`         // node id that receives the reply note
	ReplyFirstLine string   `json:"reply_first_line"` // first line of the reply; {subject} = text after the tag, {origin} = sender node
	TimeoutSeconds int      `json:"timeout_seconds"`  // default 300
	MaxInputKB     int      `json:"max_input_kb"`     // default 64
	MaxOutputKB    int      `json:"max_output_kb"`    // default 512
}

// Config is the processor's JSON configuration.
type Config struct {
	Inbox          string    `json:"inbox"`
	Outbox         string    `json:"outbox"`
	AllowedOrigins []string  `json:"allowed_origins"`
	Handlers       []Handler `json:"handlers"`
	PollSeconds    int       `json:"poll_seconds"`   // 0 = once
	StableSeconds  int       `json:"stable_seconds"` // default 5
	ReportErrors   bool      `json:"report_errors"`  // send a reply note when the command fails
	State          string    `json:"state"`
}

// Default returns a config with sensible defaults.
func Default() *Config {
	return &Config{PollSeconds: 30, StableSeconds: 5, ReportErrors: true}
}

// Load reads a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := Default()
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.State == "" {
		c.State = path + ".state"
	}
	return c, c.Validate()
}

// Validate checks the config.
func (c *Config) Validate() error {
	if c.Inbox == "" || c.Outbox == "" {
		return errors.New("processor config needs inbox and outbox")
	}
	if len(c.AllowedOrigins) == 0 {
		return errors.New("processor config needs allowed_origins (node ids whose notes may run commands)")
	}
	if len(c.Handlers) == 0 {
		return errors.New("processor config needs at least one handler")
	}
	for i := range c.Handlers {
		h := &c.Handlers[i]
		if !strings.HasPrefix(h.Tag, "#") || len(h.Tag) < 2 {
			return fmt.Errorf("handler %d: tag must start with '#'", i)
		}
		if len(h.Command) == 0 {
			return fmt.Errorf("handler %s: command is empty", h.Tag)
		}
		if err := config.ValidID(h.ReplyTo); err != nil {
			return fmt.Errorf("handler %s: reply_to: %w", h.Tag, err)
		}
		if h.ReplyFirstLine == "" {
			h.ReplyFirstLine = "Re: {subject}"
		}
		if h.TimeoutSeconds <= 0 {
			h.TimeoutSeconds = 300
		}
		if h.MaxInputKB <= 0 {
			h.MaxInputKB = 64
		}
		if h.MaxOutputKB <= 0 {
			h.MaxOutputKB = 512
		}
	}
	if c.StableSeconds <= 0 {
		c.StableSeconds = 5
	}
	return nil
}

// ExecFunc runs a command with stdin and returns stdout and stderr (tests override it).
type ExecFunc func(ctx context.Context, cmd []string, stdin string) (string, string, error)

// Processor is a running processor.
type Processor struct {
	cfg  *Config
	log  *log.Logger
	seen map[string]bool
	Exec ExecFunc
}

// New builds a processor.
func New(cfg *Config, lg *log.Logger) (*Processor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if lg == nil {
		lg = log.New(os.Stdout, "", log.LstdFlags)
	}
	p := &Processor{cfg: cfg, log: lg, seen: map[string]bool{}, Exec: runCommand}
	if b, err := os.ReadFile(cfg.State); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				p.seen[l] = true
			}
		}
	}
	return p, nil
}

// Result of one note.
type Result struct {
	File    string
	Tag     string
	Origin  string
	Status  string // processed | failed | rejected
	Reason  string
	Reply   string
	Elapsed time.Duration
}

// Run polls until stop is closed (or once when PollSeconds is 0).
func (p *Processor) Run(stop <-chan struct{}) {
	var tags []string
	for _, h := range p.cfg.Handlers {
		tags = append(tags, h.Tag)
	}
	p.log.Printf("processor: watching %s for %v from %v", p.cfg.Inbox, tags, p.cfg.AllowedOrigins)
	for {
		for _, r := range p.Once() {
			switch r.Status {
			case "processed":
				p.log.Printf("processor: %s %s from %s in %s -> %s", r.Tag, filepath.Base(r.File), r.Origin, r.Elapsed.Round(time.Millisecond), r.Reply)
			default:
				p.log.Printf("processor: %s %s from %s %s: %s", r.Tag, filepath.Base(r.File), r.Origin, r.Status, r.Reason)
			}
		}
		if p.cfg.PollSeconds <= 0 {
			return
		}
		select {
		case <-stop:
			return
		case <-time.After(time.Duration(p.cfg.PollSeconds) * time.Second):
		}
	}
}

// Once scans the inbox one time.
func (p *Processor) Once() []Result {
	var out []Result
	origins, err := os.ReadDir(p.cfg.Inbox)
	if err != nil {
		return nil
	}
	stable := time.Duration(p.cfg.StableSeconds) * time.Second
	for _, o := range origins {
		if !o.IsDir() || strings.HasPrefix(o.Name(), ".") {
			continue
		}
		origin := o.Name()
		var files []string
		_ = filepath.WalkDir(filepath.Join(p.cfg.Inbox, origin), func(f string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || mailbox.IsTemp(d.Name()) {
				return nil
			}
			files = append(files, f)
			return nil
		})
		sort.Strings(files)
		for _, f := range files {
			key := "proc:" + f
			if p.seen[key] {
				continue
			}
			st, err := os.Stat(f)
			if err != nil || time.Since(st.ModTime()) < stable {
				continue
			}
			h, subject, body, ok := p.match(f, st.Size())
			if !ok {
				p.remember(key)
				continue
			}
			r := Result{File: f, Tag: h.Tag, Origin: origin}
			if !contains(p.cfg.AllowedOrigins, origin) {
				r.Status, r.Reason = "rejected", "origin "+origin+" is not allowed to run commands"
				p.remember(key)
				out = append(out, r)
				continue
			}
			p.remember(key) // never run a command twice, even if it fails
			r = p.handle(h, origin, subject, body, r)
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

// match reads the first non-empty line and finds a handler for its tag.
func (p *Processor) match(path string, size int64) (*Handler, string, string, bool) {
	maxIn := int64(0)
	for _, h := range p.cfg.Handlers {
		if int64(h.MaxInputKB)<<10 > maxIn {
			maxIn = int64(h.MaxInputKB) << 10
		}
	}
	if size > maxIn {
		return nil, "", "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", "", false
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
	if !strings.HasPrefix(first, "#") {
		return nil, "", "", false
	}
	word := strings.Fields(first)[0]
	for i := range p.cfg.Handlers {
		h := &p.cfg.Handlers[i]
		if strings.EqualFold(word, h.Tag) {
			subject := strings.TrimSpace(first[len(word):])
			var body strings.Builder
			for sc.Scan() {
				body.WriteString(sc.Text())
				body.WriteString("\n")
			}
			return h, subject, strings.TrimSpace(body.String()), true
		}
	}
	return nil, "", "", false
}

func (p *Processor) handle(h *Handler, origin, subject, body string, r Result) Result {
	prompt := subject
	if body != "" {
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += body
	}
	if strings.TrimSpace(prompt) == "" {
		r.Status, r.Reason = "rejected", "nothing after the tag"
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(h.TimeoutSeconds)*time.Second)
	defer cancel()
	start := time.Now()
	stdout, stderr, err := p.Exec(ctx, h.Command, prompt)
	r.Elapsed = time.Since(start)
	if err != nil {
		r.Status, r.Reason = "failed", err.Error()
		if stderr != "" {
			r.Reason += ": " + strings.TrimSpace(stderr)
		}
		if p.cfg.ReportErrors {
			text := fmt.Sprintf("The %s handler could not answer this request.\n\nError: %s\n\nRequest from %s:\n%s\n", h.Tag, r.Reason, origin, truncate(prompt, 2000))
			if path, werr := p.reply(h, origin, "Error: "+subjectOf(subject, body), text); werr == nil {
				r.Reply = path
			}
		}
		return r
	}
	if int64(len(stdout)) > int64(h.MaxOutputKB)<<10 {
		stdout = stdout[:int(h.MaxOutputKB)<<10] + "\n\n[output truncated]\n"
	}
	text := strings.TrimSpace(stdout)
	if text == "" {
		text = "(the command produced no output)"
	}
	text += fmt.Sprintf("\n\n---\nRequest from %s, %s:\n%s\n", origin, start.Format(time.RFC3339), truncate(prompt, 2000))
	path, err := p.reply(h, origin, subjectOf(subject, body), text)
	if err != nil {
		r.Status, r.Reason = "failed", "reply: "+err.Error()
		return r
	}
	r.Status, r.Reply = "processed", path
	return r
}

func subjectOf(subject, body string) string {
	s := subject
	if s == "" {
		if i := strings.IndexByte(body, '\n'); i > 0 {
			s = body[:i]
		} else {
			s = body
		}
	}
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		s = strings.TrimSpace(s[:80]) + "…"
	}
	if s == "" {
		s = "(no subject)"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// reply writes the outbox note the other node (or its gateway) will act on.
func (p *Processor) reply(h *Handler, origin, subject, text string) (string, error) {
	first := strings.NewReplacer("{subject}", subject, "{origin}", origin).Replace(h.ReplyFirstLine)
	first = strings.ReplaceAll(strings.ReplaceAll(first, "\r", " "), "\n", " ")
	dir := filepath.Join(p.cfg.Outbox, h.ReplyTo)
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-reply-%s.txt", strings.TrimPrefix(h.Tag, "#"), time.Now().Format("20060102-150405"))
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s-reply-%s (%d).txt", strings.TrimPrefix(h.Tag, "#"), time.Now().Format("20060102-150405"), i)
	}
	path := filepath.Join(dir, name)
	content := first + "\n\n" + text + "\n"
	tmp := filepath.Join(dir, ".amail-tmp-reply-"+name)
	if err := os.WriteFile(tmp, []byte(content), 0o664); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

func (p *Processor) remember(key string) {
	p.seen[key] = true
	f, err := os.OpenFile(p.cfg.State, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(key + "\n")
	_ = f.Close()
}

// runCommand is the default ExecFunc.
func runCommand(ctx context.Context, argv []string, stdin string) (string, string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	hideWindow(cmd)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = errors.New("timed out")
	}
	return out.String(), errb.String(), err
}
