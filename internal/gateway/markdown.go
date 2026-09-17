package gateway

// markdown.go: a small, safe converter from the Markdown-ish text that
// people (and Claude) write into HTML for styled e-mail. Everything is
// HTML-escaped first; only a handful of constructs are recognised
// (headings, bullet and numbered lists, fenced code, inline code, bold,
// italic, links, block quotes, simple tables, rules). Unknown syntax stays
// as written. Styles are inline because mail clients drop stylesheets.

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

const (
	mdFont      = "font-family:Poppins,Arial,Helvetica,sans-serif;"
	mdMonoFont  = "font-family:Consolas,Menlo,'Courier New',monospace;"
	mdP         = `<p style="margin:0 0 14px 0;">`
	mdLinkStyle = `style="color:#AB54DB;text-decoration:underline;"`
	mdCodeStyle = `style="` + mdMonoFont + `font-size:13px;background:#F3F2F7;color:#AB54DB;padding:1px 6px;border-radius:4px;"`
	mdPreStyle  = `style="` + mdMonoFont + `font-size:13px;line-height:1.5;background:#17161E;color:#ECEAF3;padding:14px 16px;border-radius:10px;overflow:auto;margin:0 0 14px 0;white-space:pre-wrap;word-break:break-word;"`
	mdListStyle = `style="margin:0 0 14px 0;padding-left:22px;"`
	mdLiStyle   = `style="margin:0 0 6px 0;"`
	mdQuote     = `style="margin:0 0 14px 0;padding:8px 14px;border-left:3px solid #AB54DB;background:#F3F2F7;color:#3B3741;border-radius:0 8px 8px 0;"`
	mdHR        = `<hr style="border:0;border-top:1px solid #ECEAF3;margin:18px 0;">`
	mdTable     = `<table role="presentation" cellpadding="0" cellspacing="0" style="border-collapse:collapse;margin:0 0 14px 0;width:100%;font-size:14px;">`
	mdTH        = `<th align="left" style="padding:8px 10px;background:#F3F2F7;color:#17161E;font-weight:600;border-bottom:1px solid #ECEAF3;">`
	mdTD        = `<td style="padding:8px 10px;border-bottom:1px solid #ECEAF3;vertical-align:top;">`
)

var (
	reHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	reBullet   = regexp.MustCompile(`^\s{0,6}[-*+]\s+(.*)$`)
	reNumber   = regexp.MustCompile(`^\s{0,6}\d{1,3}[.)]\s+(.*)$`)
	reHR       = regexp.MustCompile(`^\s*(?:-\s*){3,}$|^\s*(?:\*\s*){3,}$|^\s*(?:_\s*){3,}$`)
	reTableSep = regexp.MustCompile(`^\s*\|?(\s*:?-+:?\s*\|)*\s*:?-+:?\s*\|?\s*$`)
	reFence    = regexp.MustCompile("^\\s*(```|~~~)")
	reCode     = regexp.MustCompile("`([^`\n]+)`")
	reLink     = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	reURL      = regexp.MustCompile(`(^|[\s(])(https?://[^\s<>&]+)`)
	reBold     = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reItalic   = regexp.MustCompile(`(^|[^\w*])\*([^*\n]+)\*`)
	rePlacehld = regexp.MustCompile("\x00(\\d+)\x00")
)

// textToHTML renders text as an HTML fragment with inline styles.
func textToHTML(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var b strings.Builder
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		b.WriteString(mdP)
		for i, l := range para {
			if i > 0 {
				b.WriteString("<br>")
			}
			b.WriteString(inline(l))
		}
		b.WriteString("</p>\n")
		para = nil
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			flush()
		case reFence.MatchString(l):
			flush()
			fence := reFence.FindStringSubmatch(l)[1]
			var code []string
			for i++; i < len(lines); i++ {
				if strings.HasPrefix(strings.TrimSpace(lines[i]), fence) {
					break
				}
				code = append(code, lines[i])
			}
			b.WriteString("<pre " + mdPreStyle + ">" + html.EscapeString(strings.Join(code, "\n")) + "</pre>\n")
		case reHeading.MatchString(t):
			flush()
			m := reHeading.FindStringSubmatch(t)
			size := []string{"22px", "19px", "17px", "16px", "15px", "15px"}[len(m[1])-1]
			b.WriteString(`<h` + strconv.Itoa(len(m[1])) + ` style="` + mdFont + `font-size:` + size + `;line-height:1.35;font-weight:600;color:#17161E;margin:18px 0 10px 0;">` + inline(m[2]) + `</h` + strconv.Itoa(len(m[1])) + ">\n")
		case reHR.MatchString(t):
			flush()
			b.WriteString(mdHR + "\n")
		case reBullet.MatchString(l) || reNumber.MatchString(l):
			flush()
			tag, re := "ul", reBullet
			if reNumber.MatchString(l) {
				tag, re = "ol", reNumber
			}
			b.WriteString("<" + tag + " " + mdListStyle + ">\n")
			for ; i < len(lines) && re.MatchString(lines[i]); i++ {
				item := re.FindStringSubmatch(lines[i])[1]
				// continuation lines (indented, not a new item, not blank) belong to the item
				for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" && !reBullet.MatchString(lines[i+1]) && !reNumber.MatchString(lines[i+1]) && strings.HasPrefix(lines[i+1], "  ") {
					i++
					item += " " + strings.TrimSpace(lines[i])
				}
				b.WriteString("<li " + mdLiStyle + ">" + inline(item) + "</li>\n")
			}
			i--
			b.WriteString("</" + tag + ">\n")
		case strings.HasPrefix(t, ">"):
			flush()
			var q []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				q = append(q, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
			}
			i--
			b.WriteString("<blockquote " + mdQuote + ">" + inline(strings.Join(q, " ")) + "</blockquote>\n")
		case strings.HasPrefix(t, "|") && i+1 < len(lines) && reTableSep.MatchString(lines[i+1]):
			flush()
			b.WriteString(mdTable + "\n<tr>")
			for _, c := range tableCells(t) {
				b.WriteString(mdTH + inline(c) + "</th>")
			}
			b.WriteString("</tr>\n")
			for i += 2; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				b.WriteString("<tr>")
				for _, c := range tableCells(strings.TrimSpace(lines[i])) {
					b.WriteString(mdTD + inline(c) + "</td>")
				}
				b.WriteString("</tr>\n")
			}
			i--
			b.WriteString("</table>\n")
		default:
			para = append(para, t)
		}
	}
	flush()
	return strings.TrimSpace(b.String())
}

func tableCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	cells := strings.Split(row, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// inline escapes a line and applies code spans, links, bold and italic.
func inline(s string) string {
	var codes []string
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, html.EscapeString(m[1:len(m)-1]))
		return "\x00" + strconv.Itoa(len(codes)-1) + "\x00"
	})
	s = html.EscapeString(s)
	s = reLink.ReplaceAllString(s, `<a href="$2" `+mdLinkStyle+`>$1</a>`)
	s = reURL.ReplaceAllString(s, `$1<a href="$2" `+mdLinkStyle+`>$2</a>`)
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reItalic.ReplaceAllString(s, "$1<em>$2</em>")
	s = rePlacehld.ReplaceAllStringFunc(s, func(m string) string {
		i, _ := strconv.Atoi(m[1 : len(m)-1])
		return "<code " + mdCodeStyle + ">" + codes[i] + "</code>"
	})
	return s
}
