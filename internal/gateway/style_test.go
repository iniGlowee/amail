package gateway

import (
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextToHTML(t *testing.T) {
	cases := []struct{ in, want, forbid string }{
		{"Hello **world**", "<strong>world</strong>", ""},
		{"# Title\ntext", ">Title</h1>", ""},
		{"### Small", ">Small</h3>", ""},
		{"- one\n- two\n", "<li", "<ol"},
		{"1. a\n2) b", "<ol", "<ul"},
		{"- item\n  continued here\n- next", "item continued here", ""},
		{"```\n<script>alert(1)</script>\n```", "&lt;script&gt;alert(1)&lt;/script&gt;", "<script>"},
		{"use `x < y` here", "<code", "x < y"},
		{"see [docs](https://example.com/a?b=1&c=2)", `href="https://example.com/a?b=1&amp;c=2"`, ""},
		{"plain https://example.com/x done", `<a href="https://example.com/x"`, ""},
		{"> quoted\n> lines", "<blockquote", ""},
		{"| a | b |\n|---|---|\n| 1 | 2 |", "<th", ""},
		{"| a | b |\n|---|---|\n| 1 | 2 |", "<td", ""},
		{"first\nsecond\n\nthird", "first<br>second", ""},
		{"---", "<hr", ""},
		{"*em* and <b>not html</b>", "<em>em</em>", "<b>"},
		{"a _plain_ line", "_plain_", "<em>"},
	}
	for _, c := range cases {
		got := textToHTML(c.in)
		if !strings.Contains(got, c.want) {
			t.Errorf("%q: want %q in\n%s", c.in, c.want, got)
		}
		if c.forbid != "" && strings.Contains(got, c.forbid) {
			t.Errorf("%q: %q must not appear in\n%s", c.in, c.forbid, got)
		}
	}
}

// parts returns the decoded text/plain and text/html bodies of a message,
// searching nested multiparts, plus the top-level media type.
func parts(t *testing.T, raw []byte) (top, text, html string, attachments int) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var walk func(ct string, r io.Reader)
	walk = func(ct string, r io.Reader) {
		mt, params, err := mime.ParseMediaType(ct)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(mt, "multipart/") {
			mr := multipart.NewReader(r, params["boundary"])
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if p.Header.Get("Content-Disposition") != "" {
					attachments++
					continue
				}
				walk(p.Header.Get("Content-Type"), p)
			}
		}
		b, _ := io.ReadAll(r)
		dec, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(b)), ""))
		if err != nil {
			t.Fatalf("part %s: %v", mt, err)
		}
		switch mt {
		case "text/plain":
			text = string(dec)
		case "text/html":
			html = string(dec)
		}
	}
	top = m.Header.Get("Content-Type")
	walk(top, m.Body)
	return
}

func TestOutboundStyled(t *testing.T) {
	c := outCfg(t)
	custom := filepath.Join(t.TempDir(), "mine.html")
	if err := os.WriteFile(custom, []byte("<h1>MINE {{.Subject}}</h1>{{.Body}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.EmailStyles = map[string]string{"mine": custom}
	g, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	sent := map[string][]byte{}
	g.Send = func(_ context.Context, msg []byte, to []string) (string, error) {
		m, _ := mail.ReadMessage(strings.NewReader(string(msg)))
		subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
		sent[subj] = msg
		return "id", nil
	}
	in := filepath.Join(c.Inbox, "austin-pc")
	write(t, filepath.Join(in, "a.txt"), "#email style:geex Claude: Styled one\n\nHello **bold** & <tag>\n\n- a\n- b\n")
	write(t, filepath.Join(in, "b.txt"), "#email to:james@example.com style:mine Custom one\n\nbody\n")
	write(t, filepath.Join(in, "c.txt"), "#email style:nope Bad style\n\nbody\n")
	write(t, filepath.Join(in, "d.txt"), "#email Plain one\n\nbody\n")
	dir := filepath.Join(in, "with-files")
	write(t, filepath.Join(dir, "message.txt"), "#email style:geex With attachment\n\nsee file\n")
	write(t, filepath.Join(dir, "file.bin"), "xyz")

	res := g.Once()
	status := map[string]string{}
	for _, r := range res {
		status[filepath.Base(r.File)] = r.Status + ":" + r.Reason
	}
	if !strings.Contains(status["c.txt"], "unknown e-mail style") {
		t.Fatalf("unknown style not refused: %v", status)
	}
	for _, f := range []string{"a.txt", "b.txt", "d.txt", "message.txt"} {
		if !strings.HasPrefix(status[f], "sent") {
			t.Fatalf("%s: %s", f, status[f])
		}
	}

	top, text, html, att := parts(t, sent["Claude: Styled one"])
	if !strings.HasPrefix(top, "multipart/alternative") || att != 0 {
		t.Fatalf("styled note: top %q attachments %d", top, att)
	}
	if !strings.Contains(text, "Hello **bold** & <tag>") {
		t.Fatalf("text part lost the original: %q", text)
	}
	for _, want := range []string{"<strong>bold</strong>", "&amp; &lt;tag&gt;", "<li", "Claude: Styled one", "from node austin-pc", "info@ausa.dev", "#AB54DB"} {
		if !strings.Contains(html, want) {
			t.Fatalf("html part lacks %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "<tag>") {
		t.Fatal("raw tag leaked into html")
	}

	_, _, html, _ = parts(t, sent["Custom one"])
	if !strings.HasPrefix(html, "<h1>MINE Custom one</h1>") {
		t.Fatalf("custom template not used: %q", html)
	}

	top, text, html, _ = parts(t, sent["Plain one"])
	if !strings.HasPrefix(top, "text/plain") || html != "" || !strings.Contains(text, "body") {
		t.Fatalf("plain note changed: top %q html %q", top, html)
	}

	top, text, html, att = parts(t, sent["With attachment"])
	if !strings.HasPrefix(top, "multipart/mixed") || att != 1 || !strings.Contains(html, "see file") || !strings.Contains(text, "see file") {
		t.Fatalf("styled with attachment: top %q att %d html %d bytes", top, att, len(html))
	}

	// default style applies when the line carries none
	c2 := outCfg(t)
	c2.EmailDefaultStyle = BuiltinStyle
	g2, err := New(c2, nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	g2.Send = func(_ context.Context, msg []byte, _ []string) (string, error) { raw = msg; return "id", nil }
	write(t, filepath.Join(c2.Inbox, "austin-pc", "e.txt"), "#email Default styled\n\nhi\n")
	g2.Once()
	if top, _, html, _ := parts(t, raw); !strings.HasPrefix(top, "multipart/alternative") || !strings.Contains(html, "Default styled") {
		t.Fatalf("default style not applied: %q", top)
	}
}

func TestOutboundStyleValidate(t *testing.T) {
	c := outCfg(t)
	c.EmailDefaultStyle = "missing"
	if _, err := New(c, nil); err == nil || !strings.Contains(err.Error(), "email_default_style") {
		t.Fatalf("bad default style accepted: %v", err)
	}
}

// TestStylePreview writes a rendered sample to $AMAIL_STYLE_PREVIEW (a file
// path) so the built-in template can be eyeballed in a browser. Skipped
// otherwise.
func TestStylePreview(t *testing.T) {
	out := os.Getenv("AMAIL_STYLE_PREVIEW")
	if out == "" {
		t.Skip("set AMAIL_STYLE_PREVIEW=/path/preview.html to write a sample")
	}
	c := outCfg(t)
	g, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := "Here is a quick summary of what a **scheduled task** on Windows can do, and how AMail uses it.\n\n" +
		"## What it does\n\n- Starts `amailw.exe` at your logon, hidden\n- Restarts it a minute after a crash\n- Never wakes the PC\n\n" +
		"### Check it\n\n```powershell\nGet-ScheduledTask AMail | Select State\namail status\n```\n\n" +
		"| Setting | Value |\n|---|---|\n| Trigger | at logon |\n| Wake | no |\n| Battery | allowed |\n\n" +
		"> Tip: re-run the installer with a new `-Binary` to upgrade.\n\nMore in the guide: [JOINING.md](https://github.com/iniGlowee/amail/blob/main/docs/JOINING.md), or *ask again* by e-mail.\n\nLine one of a paragraph\nline two of the same paragraph."
	h, err := g.renderStyle(BuiltinStyle, &Outgoing{Origin: "austin-pc", Subject: "Claude: What does the AMail scheduled task do?", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte(h), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadToleratesBOM(t *testing.T) {
	p := filepath.Join(t.TempDir(), "g.json")
	inbox := filepath.ToSlash(t.TempDir())
	js := `{"inbox": "` + inbox + `", "send_command": ["x"], "email_from": "a@b.example", "email_to": ["c@d.example"], "email_origins": ["n"]}`
	if err := os.WriteFile(p, append([]byte{0xEF, 0xBB, 0xBF}, js...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("BOM config refused: %v", err)
	}
}
