package gateway

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// flagged appends the ":2,S" suffix mail readers add in cur/. Windows
// cannot have ':' in file names (it would create an NTFS stream), so the
// suffix is only used where the test runs on a Unix file system.
func flagged(name string) string {
	if runtime.GOOS == "windows" {
		return name
	}
	return name + ":2,S"
}

const authOK = "Received-SPF: pass (spfCheck: domain of live.com designates 1.2.3.4 as permitted sender)\r\nAuthentication-Results: amazonses.com;\r\n spf=pass (spfCheck: domain of live.com designates 1.2.3.4 as permitted sender) client-ip=1.2.3.4;\r\n dkim=pass header.i=@live.com;\r\n dmarc=pass header.from=live.com;\r\nX-SES-Spam-Verdict: PASS\r\nX-SES-Virus-Verdict: PASS\r\n"

func msg(from, subject, extraHeaders, body string) string {
	return "Return-Path: <" + from + ">\r\n" + extraHeaders + "From: Someone <" + from + ">\r\nTo: info@ausa.dev\r\nSubject: " + subject + "\r\nDate: Thu, 17 Sep 2026 07:19:28 +0000\r\nMessage-ID: <abc@x>\r\n" + body
}

func multipartMsg(from, subject, extra string) string {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	return msg(from, subject, extra+"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"OUTER\"\r\n", "\r\n"+
		"--OUTER\r\nContent-Type: multipart/alternative; boundary=\"INNER\"\r\n\r\n"+
		"--INNER\r\nContent-Type: text/plain; charset=\"utf-8\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nHello caf=C3=A9,\r\nsee attached.\r\n"+
		"--INNER\r\nContent-Type: text/html; charset=\"utf-8\"\r\n\r\n<p>Hello caf&eacute;</p>\r\n--INNER--\r\n"+
		"--OUTER\r\nContent-Type: image/png; name=\"photo.png\"\r\nContent-Disposition: attachment; filename=\"photo.png\"\r\nContent-Transfer-Encoding: base64\r\n\r\n"+png+"\r\n"+
		"--OUTER\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"=?UTF-8?Q?r=C3=A9sum=C3=A9=2Epdf?=\"\r\nContent-Transfer-Encoding: base64\r\n\r\n"+base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 fake"))+"\r\n"+
		"--OUTER--\r\n")
}

func cfg(t *testing.T) *Config {
	c := Default()
	c.Maildir = filepath.Join(t.TempDir(), "Maildir")
	c.Outbox = filepath.Join(t.TempDir(), "outbox")
	c.AllowedSenders = []string{"austin_armas@live.com", "@ausa.dev"}
	c.State = filepath.Join(t.TempDir(), "state")
	c.PollSeconds = 0
	for _, d := range []string{"new", "cur", "tmp"} {
		_ = os.MkdirAll(filepath.Join(c.Maildir, d), 0o755)
	}
	return c
}

func TestParseQualifies(t *testing.T) {
	c := cfg(t)
	p, reason, err := Parse(strings.NewReader(multipartMsg("austin_armas@live.com", "Site photos #amail austin-pc for the new page", authOK)), c)
	if err != nil || reason != "" {
		t.Fatalf("err=%v reason=%q", err, reason)
	}
	if p.To != "austin-pc" || p.Title != "Site photos for the new page" || p.From != "austin_armas@live.com" {
		t.Fatalf("parsed: %+v", p)
	}
	if !strings.Contains(p.Text, "Hello café") || p.HTML == "" {
		t.Fatalf("body: %q", p.Text)
	}
	if len(p.Attachments) != 2 || p.Attachments[0].Name != "photo.png" || p.Attachments[1].Name != "résumé.pdf" {
		t.Fatalf("attachments: %+v", p.Attachments)
	}
	if string(p.Attachments[0].Data[:4]) != "\x89PNG" || string(p.Attachments[1].Data) != "%PDF-1.4 fake" {
		t.Fatal("attachment content not decoded")
	}
	if !strings.Contains(p.Headers, "Routed-To: austin-pc") || !strings.Contains(p.Headers, "dkim=pass") {
		t.Fatalf("headers: %s", p.Headers)
	}
}

func TestParseRejects(t *testing.T) {
	c := cfg(t)
	cases := []struct {
		name, from, subject, extra, want string
	}{
		{"no tag", "austin_armas@live.com", "just hello", authOK, "no #amail tag"},
		{"no node", "austin_armas@live.com", "hello #amail", authOK, "without a node id"},
		{"bad node", "austin_armas@live.com", "#amail Bad_Node", authOK, "bad node id"},
		{"stranger", "evil@example.com", "#amail austin-pc hi", authOK, "not on the allow-list"},
		{"no auth", "austin_armas@live.com", "#amail austin-pc hi", "", "no passing SPF/DKIM"},
		{"spf fail only", "austin_armas@live.com", "#amail austin-pc hi", "Authentication-Results: amazonses.com; spf=fail; dkim=fail\r\n", "no passing SPF/DKIM"},
		{"virus", "austin_armas@live.com", "#amail austin-pc hi", authOK + "X-SES-Virus-Verdict: FAIL\r\n", "X-SES-Virus-Verdict"},
	}
	for _, tc := range cases {
		_, reason, err := Parse(strings.NewReader(msg(tc.from, tc.subject, tc.extra, "\r\nbody\r\n")), c)
		if err != nil || !strings.Contains(reason, tc.want) {
			t.Errorf("%s: err=%v reason=%q want %q", tc.name, err, reason, tc.want)
		}
	}
	// domain allow and secret
	c.Secret = "hunter2"
	if _, reason, _ := Parse(strings.NewReader(msg("someone@ausa.dev", "#amail austin-pc hi", authOK, "\r\nx\r\n")), c); !strings.Contains(reason, "secret") {
		t.Fatalf("secret not enforced: %q", reason)
	}
	p, reason, _ := Parse(strings.NewReader(msg("someone@ausa.dev", "#amail austin-pc hunter2 the title", authOK, "\r\nx\r\n")), c)
	if reason != "" || p.Title != "the title" {
		t.Fatalf("secret path: %q %+v", reason, p)
	}
	// require_auth off accepts unauthenticated allow-listed senders
	c.RequireAuth = false
	c.Secret = ""
	if _, reason, _ := Parse(strings.NewReader(msg("austin_armas@live.com", "#amail austin-pc hi", "", "\r\nx\r\n")), c); reason != "" {
		t.Fatalf("require_auth=false: %q", reason)
	}
}

func TestOnceWritesAndRemembers(t *testing.T) {
	c := cfg(t)
	g, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(c.Maildir, "new", "1700000000.1.host")
	_ = os.WriteFile(good, []byte(multipartMsg("austin_armas@live.com", "#amail austin-pc Site photos", authOK)), 0o600)
	_ = os.WriteFile(filepath.Join(c.Maildir, "new", "1700000001.2.host"), []byte(msg("austin_armas@live.com", "ordinary mail", authOK, "\r\nhi\r\n")), 0o600)
	_ = os.WriteFile(filepath.Join(c.Maildir, "cur", flagged("1700000002.3.host")), []byte(msg("evil@example.com", "#amail austin-pc pwn", authOK, "\r\nhi\r\n")), 0o600)

	res := g.Once()
	var conv, rej int
	var dest string
	for _, r := range res {
		switch r.Status {
		case "converted":
			conv++
			dest = r.Dest
		case "rejected":
			rej++
		}
	}
	if conv != 1 || rej != 1 {
		t.Fatalf("results: %+v", res)
	}
	if !strings.HasPrefix(filepath.Base(dest), "Site photos-") || filepath.Base(filepath.Dir(dest)) != "austin-pc" {
		t.Fatalf("dest %s", dest)
	}
	for _, f := range []string{"message.txt", "headers.txt", "photo.png", "résumé.pdf"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Fatalf("missing %s", f)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dest, "message.txt"))
	if !strings.HasPrefix(string(b), "Hello café") {
		t.Fatalf("message.txt: %q", b)
	}
	// second pass: nothing new, even after the reader moved the mail to cur/ with flags
	_ = os.Rename(good, filepath.Join(c.Maildir, "cur", flagged("1700000000.1.host")))
	if res := g.Once(); len(res) != 0 {
		t.Fatalf("reprocessed: %+v", res)
	}
	// a fresh gateway instance reads the state file and also skips it
	g2, _ := New(c, nil)
	if res := g2.Once(); len(res) != 0 {
		t.Fatalf("state not persisted: %+v", res)
	}
	// the original mail is untouched
	if _, err := os.Stat(filepath.Join(c.Maildir, "cur", flagged("1700000000.1.host"))); err != nil {
		t.Fatal("mail was moved or deleted")
	}
}

func TestConfigValidate(t *testing.T) {
	c := Default()
	if err := c.Validate(); err == nil {
		t.Fatal("empty config accepted")
	}
	c.Maildir, c.Outbox = "/m", "/o"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "allowed sender") {
		t.Fatalf("open gateway accepted: %v", err)
	}
	c.AllowedSenders = []string{"a@b.c"}
	c.Tag = "amail"
	if err := c.Validate(); err == nil {
		t.Fatal("tag without # accepted")
	}
}
