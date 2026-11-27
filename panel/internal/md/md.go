// Package md turns the panel's own Markdown (the guides and the CHANGELOG, HELP-01) into HTML. It knows the small
// part of Markdown those files use: headings, paragraphs, lists (nested by indent), fenced code, tables, quotes,
// `code`, **bold**, *italic* and [links](…). Every piece of text is escaped first, and no raw HTML passes; a link
// goes through Link, which decides where it may point.
package md

import (
	"html"
	"regexp"
	"strings"
)

// Link maps a link target to an href; "" keeps only the link's text.
type Link func(target string) string

// Heading is a heading of the page, with its anchor.
type Heading struct {
	Level int
	Text  string
	ID    string
}

var (
	headRe  = regexp.MustCompile(`^(#{1,4})\s+(.+?)\s*#*\s*$`)
	itemRe  = regexp.MustCompile(`^(\s*)([-*+]|[0-9]{1,3}[.)])\s+(.*)$`)
	sepRe   = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$`)
	fenceRe = regexp.MustCompile("^(\\s*)(```|~~~)")
	slugRe  = regexp.MustCompile(`[^a-z0-9]+`)
	// inline: code first, then links, then bold and italic
	codeRe   = regexp.MustCompile("`([^`]+)`")
	linkRe   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	boldRe   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	italicRe = regexp.MustCompile(`(^|[\s(])\*([^*\s][^*]*)\*`)
)

// Slug makes a heading's anchor: "[1.49.0] — 2026-11-27" -> "1-49-0-2026-11-27".
func Slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

type renderer struct {
	b     strings.Builder
	link  Link
	heads []Heading
	ids   map[string]int
	para  []string
	lists []listLevel // open lists, outermost first
	quote []string
}

type listLevel struct {
	indent  int
	ordered bool
	item    []string // text of the open item
}

// Render returns the HTML of src and its headings.
func Render(src string, link Link) (string, []Heading) {
	r := &renderer{link: link, ids: map[string]int{}}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		t := strings.TrimSpace(ln)
		switch {
		case fenceRe.MatchString(ln):
			m := fenceRe.FindStringSubmatch(ln)
			var code []string
			for i++; i < len(lines); i++ {
				if strings.HasPrefix(strings.TrimSpace(lines[i]), m[2]) {
					break
				}
				code = append(code, strings.TrimPrefix(lines[i], m[1]))
			}
			if len(r.lists) > 0 && len(m[1]) > 0 { // code inside a list item
				r.flushItemText()
				r.b.WriteString(`<pre><code>` + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>")
				continue
			}
			r.closeAll()
			r.b.WriteString(`<pre><code>` + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case t == "":
			r.closePara()
			r.closeQuote()
			// a list stays open across a blank line when the next line goes on with it
			if len(r.lists) > 0 {
				r.flushItemText()
				next := ""
				for k := i + 1; k < len(lines); k++ {
					if strings.TrimSpace(lines[k]) != "" {
						next = lines[k]
						break
					}
				}
				if !itemRe.MatchString(next) && indentOf(next) <= r.lists[0].indent {
					r.closeLists(0)
				}
			}
		case headRe.MatchString(ln) && indentOf(ln) == 0:
			r.closeAll()
			m := headRe.FindStringSubmatch(ln)
			lvl := len(m[1]) + 1 // the page title is the page's h1
			if lvl > 5 {
				lvl = 5
			}
			text := m[2]
			id := Slug(plain(text))
			if n := r.ids[id]; n > 0 {
				r.ids[id] = n + 1
				id = id + "-" + itoa(n+1)
			} else {
				r.ids[id] = 1
			}
			r.heads = append(r.heads, Heading{Level: len(m[1]), Text: plain(text), ID: id})
			r.b.WriteString("<h" + itoa(lvl) + ` id="` + id + `">` + r.inline(text) + "</h" + itoa(lvl) + ">\n")
		case strings.HasPrefix(t, "|") && i+1 < len(lines) && sepRe.MatchString(lines[i+1]):
			r.closeAll()
			r.b.WriteString(`<div class="table-wrap"><table><thead><tr>`)
			for _, c := range cells(t) {
				r.b.WriteString("<th>" + r.inline(c) + "</th>")
			}
			r.b.WriteString("</tr></thead><tbody>")
			for i += 2; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				r.b.WriteString("<tr>")
				for _, c := range cells(strings.TrimSpace(lines[i])) {
					r.b.WriteString("<td>" + r.inline(c) + "</td>")
				}
				r.b.WriteString("</tr>")
			}
			i--
			r.b.WriteString("</tbody></table></div>\n")
		case strings.HasPrefix(t, ">") && len(r.lists) == 0:
			r.closePara()
			r.quote = append(r.quote, strings.TrimSpace(strings.TrimPrefix(t, ">")))
		case itemRe.MatchString(ln):
			r.closePara()
			r.closeQuote()
			m := itemRe.FindStringSubmatch(ln)
			r.item(indentOf(m[1]), m[2] != "-" && m[2] != "*" && m[2] != "+", m[3])
		case len(r.lists) > 0 && indentOf(ln) > r.lists[0].indent:
			top := &r.lists[len(r.lists)-1]
			top.item = append(top.item, t)
		default:
			if len(r.lists) > 0 {
				r.closeLists(0)
			}
			r.closeQuote()
			r.para = append(r.para, t)
		}
	}
	r.closeAll()
	return r.b.String(), r.heads
}

func indentOf(s string) int {
	n := 0
	for _, c := range s {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func cells(row string) []string {
	row = strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
	// a pipe inside `code` stays in its cell
	var out []string
	var cur strings.Builder
	in := false
	for _, c := range row {
		switch {
		case c == '`':
			in = !in
			cur.WriteRune(c)
		case c == '|' && !in:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	return append(out, strings.TrimSpace(cur.String()))
}

func (r *renderer) item(indent int, ordered bool, text string) {
	// close deeper lists
	for len(r.lists) > 0 && r.lists[len(r.lists)-1].indent > indent {
		r.closeLists(len(r.lists) - 1)
	}
	if n := len(r.lists); n > 0 && r.lists[n-1].indent == indent {
		top := &r.lists[n-1]
		r.flushItemText()
		r.b.WriteString("</li>")
		if top.ordered != ordered { // another kind of list at the same level
			r.closeLists(n - 1)
		} else {
			r.b.WriteString("<li>")
			top.item = []string{text}
			return
		}
	} else if n > 0 {
		r.flushItemText() // a sub-list starts inside the open item
	}
	tag := "<ul>"
	if ordered {
		tag = "<ol>"
	}
	r.b.WriteString(tag + "<li>")
	r.lists = append(r.lists, listLevel{indent: indent, ordered: ordered, item: []string{text}})
}

func (r *renderer) flushItemText() {
	if n := len(r.lists); n > 0 && len(r.lists[n-1].item) > 0 {
		r.b.WriteString(r.inline(strings.Join(r.lists[n-1].item, " ")))
		r.lists[n-1].item = nil
	}
}

// closeLists closes the lists from level k on.
func (r *renderer) closeLists(k int) {
	for len(r.lists) > k {
		r.flushItemText()
		top := r.lists[len(r.lists)-1]
		r.lists = r.lists[:len(r.lists)-1]
		if top.ordered {
			r.b.WriteString("</li></ol>")
		} else {
			r.b.WriteString("</li></ul>")
		}
	}
	if k == 0 {
		r.b.WriteString("\n")
	}
}

func (r *renderer) closePara() {
	if len(r.para) > 0 {
		r.b.WriteString("<p>" + r.inline(strings.Join(r.para, " ")) + "</p>\n")
		r.para = nil
	}
}

func (r *renderer) closeQuote() {
	if len(r.quote) > 0 {
		r.b.WriteString("<blockquote><p>" + r.inline(strings.Join(r.quote, " ")) + "</p></blockquote>\n")
		r.quote = nil
	}
}

func (r *renderer) closeAll() {
	r.closePara()
	r.closeQuote()
	if len(r.lists) > 0 {
		r.closeLists(0)
	}
}

// inline escapes s and then marks up code, links, bold and italic. Code spans are cut out first so nothing inside
// them is marked up.
func (r *renderer) inline(s string) string {
	var codes []string
	s = codeRe.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, "<code>"+html.EscapeString(m[1:len(m)-1])+"</code>")
		return "\x00" + itoa(len(codes)-1) + "\x00"
	})
	var links []string
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		p := linkRe.FindStringSubmatch(m)
		text := r.emph(html.EscapeString(p[1]))
		href := ""
		if r.link != nil {
			href = r.link(p[2])
		}
		out := text
		if href != "" {
			ext := ""
			if strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "http://") {
				ext = ` target="_blank" rel="noopener noreferrer"`
			}
			out = `<a href="` + html.EscapeString(href) + `"` + ext + ">" + text + "</a>"
		}
		links = append(links, out)
		return "\x01" + itoa(len(links)-1) + "\x01"
	})
	s = r.emph(html.EscapeString(s))
	s = restore(s, "\x01", links)
	return restore(s, "\x00", codes)
}

func (r *renderer) emph(s string) string {
	s = boldRe.ReplaceAllString(s, "<b>$1</b>")
	return italicRe.ReplaceAllString(s, "$1<i>$2</i>")
}

func restore(s, mark string, parts []string) string {
	for i, p := range parts {
		s = strings.Replace(s, mark+itoa(i)+mark, p, 1)
	}
	return s
}

// plain is a heading's text without its markup.
func plain(s string) string {
	s = linkRe.ReplaceAllString(s, "$1")
	return strings.NewReplacer("`", "", "**", "", "*", "").Replace(s)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}
