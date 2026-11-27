package md

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func link(t string) string {
	if strings.HasPrefix(t, "https://") {
		return t
	}
	if strings.HasSuffix(t, ".md") {
		return "/help/" + strings.TrimSuffix(filepath.Base(t), ".md")
	}
	return ""
}

func TestRender(t *testing.T) {
	src := "# Title `x`\n\nSome **bold** and *it* text\nsecond line with [guide](docs/guides/update.md).\n\n" +
		"## Steps\n1. one\n   more of one\n2. two\n   - sub a\n   - sub b\n3. three\n\n   ```bash\n   echo hi\n   ```\n\n" +
		"| A | B |\n|---|---|\n| `a|b` | [x](https://semver.org/) |\n\n> **Rule:** keep it\n> open\n\n## Steps\n- [bad](javascript:alert) <script>x</script>\n"
	out, heads := Render(src, link)
	for _, want := range []string{
		`<h2 id="title-x">Title <code>x</code></h2>`,
		`<p>Some <b>bold</b> and <i>it</i> text second line with <a href="/help/update">guide</a>.</p>`,
		`<ol><li>one more of one</li><li>two<ul><li>sub a</li><li>sub b</li></ul></li><li>three<pre><code>echo hi</code></pre></li></ol>`,
		`<td><code>a|b</code></td><td><a href="https://semver.org/" target="_blank" rel="noopener noreferrer">x</a></td>`,
		`<blockquote><p><b>Rule:</b> keep it open</p></blockquote>`,
		`<h3 id="steps-2">Steps</h3>`,
		`<li>bad &lt;script&gt;x&lt;/script&gt;</li>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %s\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "javascript") || strings.Contains(out, "<script") {
		t.Errorf("unsafe output: %s", out)
	}
	if len(heads) != 3 || heads[0].Text != "Title x" || heads[2].ID != "steps-2" {
		t.Errorf("heads %+v", heads)
	}
}

// Every guide and the CHANGELOG render with balanced tags and no raw HTML from the source.
func TestRealDocs(t *testing.T) {
	files, _ := filepath.Glob("../../../docs/guides/*.md")
	files = append(files, "../../../CHANGELOG.md")
	if len(files) < 10 {
		t.Fatalf("docs not found: %v", files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out, heads := Render(string(b), link)
		if len(heads) == 0 {
			t.Errorf("%s: no headings", f)
		}
		for _, tag := range []string{"ul", "ol", "li", "p", "pre", "table", "blockquote", "code", "b", "i", "a"} {
			o, c := strings.Count(out, "<"+tag+">")+strings.Count(out, "<"+tag+" "), strings.Count(out, "</"+tag+">")
			if o != c {
				t.Errorf("%s: <%s> %d open, %d closed", f, tag, o, c)
			}
		}
		if strings.Contains(out, "**") || strings.Contains(out, "](") {
			i := strings.Index(out, "**")
			if i < 0 {
				i = strings.Index(out, "](")
			}
			t.Errorf("%s: markup left: %q", f, out[max(0, i-80):min(len(out), i+80)])
		}
	}
}
