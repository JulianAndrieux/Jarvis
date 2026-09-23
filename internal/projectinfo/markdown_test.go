package projectinfo

import (
	"strings"
	"testing"
)

func TestRenderMarkdown_ListsNestedWithContinuations(t *testing.T) {
	src := "- **Jalon** un\n  suite de un\n  - sous `code`\n- deux ~~barré~~\n\nParagraphe\nsur deux lignes."
	got := RenderMarkdown(src)
	want := "<ul><li><strong>Jalon</strong> un suite de un<ul><li>sous <code>code</code></li></ul></li><li>deux <del>barré</del></li></ul><p>Paragraphe sur deux lignes.</p>"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestRenderMarkdown_OrderedHeadingsAndFence(t *testing.T) {
	src := "### Titre\n1. premier\n2. second\n```\nx := <b>\n  indenté\n```"
	got := RenderMarkdown(src)
	for _, want := range []string{"<h4>Titre</h4>", "<ol><li>premier</li><li>second</li></ol>", "<pre><code>x := &lt;b&gt;\n  indenté</code></pre>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

// CLAUDE.md est tenu à la main, mais le rendu ne doit jamais faire
// confiance à son contenu.
func TestRenderMarkdown_EscapesEverything(t *testing.T) {
	got := RenderMarkdown("- <script>alert(1)</script> **<img src=x>** `<i>`")
	if strings.Contains(got, "<script") || strings.Contains(got, "<img") || strings.Contains(got, "<i>") {
		t.Errorf("unescaped HTML: %s", got)
	}
}

func TestRenderMarkdown_InlineCodeProtectsItsContent(t *testing.T) {
	got := RenderMarkdown("voir `**pas gras**` et **gras**")
	want := "<p>voir <code>**pas gras**</code> et <strong>gras</strong></p>"
	if got != want {
		t.Errorf("got %s", got)
	}
}

// Vu sur CLAUDE.md : un passage en gras qui contient du code.
func TestRenderMarkdown_BoldAroundCode(t *testing.T) {
	got := RenderMarkdown("**Interface web (`cmd/jarvisapp`) : stockée.** Suite")
	want := "<p><strong>Interface web (<code>cmd/jarvisapp</code>) : stockée.</strong> Suite</p>"
	if got != want {
		t.Errorf("got %s", got)
	}
}
