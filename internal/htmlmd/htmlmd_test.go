package htmlmd

import (
	"strings"
	"testing"
)

func TestConvertHeadingsAndParagraphs(t *testing.T) {
	got := Convert(`<h1>Title</h1><p>Hello <strong>world</strong>.</p>`)
	if !strings.Contains(got, "# Title") {
		t.Errorf("missing h1: %q", got)
	}
	if !strings.Contains(got, "**world**") {
		t.Errorf("missing bold: %q", got)
	}
}

func TestConvertLists(t *testing.T) {
	got := Convert(`<ul><li>one</li><li>two</li></ul>`)
	if !strings.Contains(got, "- one") || !strings.Contains(got, "- two") {
		t.Errorf("ul not rendered: %q", got)
	}

	got = Convert(`<ol><li>first</li><li>second</li></ol>`)
	if !strings.Contains(got, "1. first") || !strings.Contains(got, "2. second") {
		t.Errorf("ol not rendered: %q", got)
	}
}

func TestConvertLinksAndImages(t *testing.T) {
	got := Convert(`<p>See <a href="https://example.com">site</a>.</p>`)
	if !strings.Contains(got, "[site](https://example.com)") {
		t.Errorf("link: %q", got)
	}

	got = Convert(`<img src="https://example.com/x.png" alt="alt text">`)
	if !strings.Contains(got, "![alt text](https://example.com/x.png)") {
		t.Errorf("image: %q", got)
	}
}

func TestConvertBlockquoteAndCode(t *testing.T) {
	got := Convert(`<blockquote><p>Quoted</p></blockquote>`)
	if !strings.Contains(got, "> Quoted") {
		t.Errorf("blockquote: %q", got)
	}

	got = Convert(`<pre><code>line1
line2</code></pre>`)
	if !strings.Contains(got, "```") || !strings.Contains(got, "line1") {
		t.Errorf("pre: %q", got)
	}
}

func TestConvertSkipsScriptAndNav(t *testing.T) {
	got := Convert(`<p>keep</p><script>drop me</script><nav>menu</nav>`)
	if strings.Contains(got, "drop") || strings.Contains(got, "menu") {
		t.Errorf("did not skip: %q", got)
	}
}

func TestConvertEscapesMarkdownChars(t *testing.T) {
	got := Convert(`<p>use *stars* and _underscores_</p>`)
	if !strings.Contains(got, `\*stars\*`) || !strings.Contains(got, `\_underscores\_`) {
		t.Errorf("escapes: %q", got)
	}
}
