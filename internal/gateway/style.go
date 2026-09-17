package gateway

// style.go: HTML e-mail styles. A "#email style:NAME Subject" line (or
// email_default_style) makes the gateway send a multipart/alternative
// message: the plain text as written, plus an HTML part rendered from a
// template. The built-in style "geex" follows the Geex admin theme (Poppins,
// purple #AB54DB, soft grey #F3F2F7) with every style inline, which is what
// Outlook.com, Gmail and the rest actually render. Operators can add their
// own templates under email_styles (name -> file), Go html/template syntax
// with .Subject .Origin .From .Date .Body (rendered HTML) and .Text.

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"time"
)

// BuiltinStyle is the name of the bundled Geex-flavoured template.
const BuiltinStyle = "geex"

type styleData struct {
	Subject string
	Origin  string
	From    string
	Date    string
	Body    template.HTML
	Text    string
}

// renderStyle returns the HTML body for og in the named style.
func (g *Gateway) renderStyle(name string, og *Outgoing) (string, error) {
	src := builtinGeex
	path, custom := g.cfg.EmailStyles[name]
	switch {
	case custom && path != "":
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("style %q: %v", name, err)
		}
		src = string(b)
	case custom || name == BuiltinStyle:
		// built-in
	default:
		return "", fmt.Errorf("unknown e-mail style %q (built-in: %s; others go under email_styles)", name, BuiltinStyle)
	}
	t, err := template.New(name).Parse(src)
	if err != nil {
		return "", fmt.Errorf("style %q: %v", name, err)
	}
	data := styleData{
		Subject: og.Subject,
		Origin:  og.Origin,
		From:    g.cfg.EmailFrom,
		Date:    time.Now().Format("2 January 2006, 15:04 MST"),
		Body:    template.HTML(textToHTML(og.Body)),
		Text:    og.Body,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("style %q: %v", name, err)
	}
	return buf.String(), nil
}

const builtinGeex = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light">
<title>{{.Subject}}</title>
</head>
<body style="margin:0;padding:0;background:#F3F2F7;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#F3F2F7;">
<tr><td align="center" style="padding:28px 12px;">
<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="max-width:600px;width:100%;">
  <tr>
    <td style="padding:0 6px 14px 6px;font-family:Poppins,Arial,Helvetica,sans-serif;font-size:13px;line-height:20px;color:#A3A3A3;">
      <span style="display:inline-block;width:12px;height:12px;border-radius:4px;background:#AB54DB;vertical-align:middle;margin:0 8px 2px 0;"></span><span style="font-weight:600;color:#17161E;vertical-align:middle;">AMail</span><span style="vertical-align:middle;">&nbsp;&nbsp;&middot;&nbsp;&nbsp;from node {{.Origin}}</span>
    </td>
  </tr>
  <tr>
    <td style="background:#ffffff;border-radius:16px;">
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
        <tr><td style="background:#AB54DB;height:6px;font-size:0;line-height:0;border-radius:16px 16px 0 0;">&nbsp;</td></tr>
        <tr>
          <td style="padding:28px 32px 6px 32px;font-family:Poppins,Arial,Helvetica,sans-serif;font-size:20px;line-height:30px;font-weight:600;color:#17161E;">{{.Subject}}</td>
        </tr>
        <tr>
          <td style="padding:0 32px 14px 32px;"><div style="height:1px;background:#ECEAF3;font-size:0;line-height:0;">&nbsp;</div></td>
        </tr>
        <tr>
          <td style="padding:0 32px 26px 32px;font-family:Poppins,Arial,Helvetica,sans-serif;font-size:15px;line-height:25px;color:#464255;">
{{.Body}}
          </td>
        </tr>
      </table>
    </td>
  </tr>
  <tr>
    <td style="padding:16px 6px 0 6px;font-family:Poppins,Arial,Helvetica,sans-serif;font-size:12px;line-height:19px;color:#A3A3A3;">
      Sent by {{.From}} through the AMail gateway &middot; {{.Date}}
    </td>
  </tr>
</table>
</td></tr>
</table>
</body>
</html>
`
