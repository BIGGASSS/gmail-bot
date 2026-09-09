package formatting

import (
	"html"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BIGGASSS/gmail-bot/internal/models"
	nethtml "golang.org/x/net/html"
)

func TestRenderAndChunkLongLinks(t *testing.T) {
	shortURL := "https://example.com/?a=1&b=2"
	longURL := "https://example.com/" + strings.Repeat("x&", 5000)
	label := strings.Repeat("世界 & < 🚀", 1500)
	for _, tt := range []struct{ name, input, text, target string }{
		{"long label", EncodeLinkToken(label, shortURL), label, shortURL},
		{"long target", EncodeLinkToken("Read more", longURL), "Read more <" + longURL + ">", ""},
		{"plain long URL", longURL, longURL, ""},
		{"boundary", strings.Repeat("x", SafeByteLimit-1) + EncodeLinkToken(label, shortURL) + " end", strings.Repeat("x", SafeByteLimit-1) + label + " end", shortURL},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var text strings.Builder
			for _, chunk := range RenderAndChunk(tt.input, SafeByteLimit) {
				if len(chunk) > SafeByteLimit || !utf8.ValidString(chunk) || strings.ContainsAny(chunk, linkTokenStart+linkTokenSeparator+linkTokenEnd) {
					t.Fatalf("invalid chunk (%d bytes)", len(chunk))
				}
				tokenizer := nethtml.NewTokenizer(strings.NewReader(chunk))
				depth := 0
				for {
					kind := tokenizer.Next()
					if kind == nethtml.ErrorToken {
						if tokenizer.Err() != io.EOF {
							t.Fatal(tokenizer.Err())
						}
						break
					}
					token := tokenizer.Token()
					switch kind {
					case nethtml.TextToken:
						text.WriteString(token.Data)
					case nethtml.StartTagToken:
						if token.Data != "a" || depth != 0 || tt.target == "" {
							t.Fatalf("unexpected tag: %+v", token)
						}
						if len(token.Attr) != 1 || token.Attr[0].Key != "href" || token.Attr[0].Val != tt.target {
							t.Fatalf("corrupted target: %+v", token.Attr)
						}
						depth++
					case nethtml.EndTagToken:
						if token.Data != "a" || depth != 1 {
							t.Fatalf("unexpected closing tag: %+v", token)
						}
						depth--
					default:
						t.Fatalf("unexpected token: %+v", token)
					}
				}
				if depth != 0 {
					t.Fatal("unclosed anchor")
				}
			}
			if text.String() != tt.text {
				t.Fatalf("decoded text differs: got %d bytes, want %d", text.Len(), len(tt.text))
			}
		})
	}
}

func TestTruncateTextTokenBoundaries(t *testing.T) {
	token := EncodeLinkToken("世界 label", "https://example.com")
	value := "prefix " + token + " suffix"
	for limit := 1; limit < len([]rune(value)); limit++ {
		got := TruncateText(value, limit)
		if len([]rune(got)) > limit {
			t.Fatalf("limit %d exceeded", limit)
		}
		rendered := RenderTelegramHTML(got)
		if strings.ContainsAny(rendered, linkTokenStart+linkTokenSeparator+linkTokenEnd) {
			t.Fatalf("split token at limit %d: %q", limit, got)
		}
	}
	if got := TruncateText(token, 3); got != "世界 " {
		t.Fatalf("fallback not decoded: %q", got)
	}
}

func TestAttachmentMetadataCannotForgeTokens(t *testing.T) {
	forged := EncodeLinkToken("click", "https://evil.example")
	for _, attachment := range []models.AttachmentMeta{{Filename: forged, MimeType: "text/plain"}, {Filename: "file", MimeType: forged}} {
		value := strings.Join(FormatAttachmentLines([]models.AttachmentMeta{attachment}), "\n")
		if strings.ContainsAny(value, linkTokenStart+linkTokenSeparator+linkTokenEnd) {
			t.Fatal("attachment delimiters survived")
		}
		for _, chunk := range RenderAndChunk(value, SafeByteLimit) {
			if strings.Contains(chunk, "<a ") {
				t.Fatalf("forged anchor: %s", chunk)
			}
		}
	}
}

func TestRenderAndChunkSmallBudgetEscapesAtomically(t *testing.T) {
	input := strings.Repeat("'&世界🚀", 20)
	for limit := 6; limit < 40; limit++ {
		var decoded strings.Builder
		for _, chunk := range RenderAndChunk(input, limit) {
			if len(chunk) > limit || !utf8.ValidString(chunk) {
				t.Fatalf("invalid chunk for limit %d", limit)
			}
			decoded.WriteString(html.UnescapeString(chunk))
		}
		if decoded.String() != input {
			t.Fatalf("split entity for limit %d", limit)
		}
	}
}
