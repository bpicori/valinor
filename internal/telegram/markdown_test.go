package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatTelegramMarkdownRendersTelegramHTML(t *testing.T) {
	input := strings.Join([]string{
		"Hello **bold** _italic_ ~~strike~~ [site](https://example.com)",
		"",
		"`code`",
		"",
		"```go",
		`fmt.Println("hi")`,
		"```",
		"",
		"https://example.com",
		"",
		"<div>unsafe</div>",
	}, "\n")

	formatted := FormatTelegramMarkdown(input)

	assertContains(t, formatted.HTML, "<b>bold</b>")
	assertContains(t, formatted.HTML, "<i>italic</i>")
	assertContains(t, formatted.HTML, "<s>strike</s>")
	assertContains(t, formatted.HTML, `<a href="https://example.com">site</a>`)
	assertContains(t, formatted.HTML, "<code>code</code>")
	assertContains(t, formatted.HTML, "<pre>fmt.Println(&#34;hi&#34;)\n</pre>")
	assertContains(t, formatted.HTML, `<a href="https://example.com">https://example.com</a>`)
	assertContains(t, formatted.HTML, "&lt;div&gt;unsafe&lt;/div&gt;")

	assertContains(t, formatted.PlainText, "site (https://example.com)")
	assertContains(t, formatted.PlainText, "https://example.com")
	assertContains(t, formatted.PlainText, "<div>unsafe</div>")
}

func TestRenderFinalContentFallsBackToPlainTextWhenHTMLTooLong(t *testing.T) {
	renderer := NewRenderer()
	content := strings.Repeat("**bold text** ", 500)

	result := renderer.RenderFinalContent(content)
	if result == nil {
		t.Fatal("expected render result")
	}

	if result.ParseMode != "" {
		t.Fatalf("expected plain-text fallback parse mode, got %q", result.ParseMode)
	}
	if result.Text != result.FallbackText {
		t.Fatal("expected rendered text to match fallback text")
	}
	if utf8.RuneCountInString(result.Text) > MaxTelegramMessageLength {
		t.Fatalf("expected fallback text length <= %d, got %d", MaxTelegramMessageLength, utf8.RuneCountInString(result.Text))
	}
	if !strings.HasSuffix(result.Text, "...") {
		t.Fatalf("expected truncated fallback text to end with ellipsis, got %q", result.Text[len(result.Text)-10:])
	}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected %q to contain %q", got, want)
	}
}
