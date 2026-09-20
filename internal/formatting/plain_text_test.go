package formatting

import (
	"strings"
	"testing"

	"github.com/BIGGASSS/gmail-bot/internal/models"
)

func TestNormalizePlainText(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"paragraphs", "\n\nFirst\n\n\n\nSecond\n\n", "First\n\nSecond"},
		{"spacers", "First\n\u200a\n\u00a0\n\u200b\u200c\u200d\ufeff\u00ad\nSecond", "First\n\nSecond"},
		{"empty", "\t \n\u200a\r\n\ufeff", ""},
		{"line endings", "First\r\nSecond\rThird", "First\nSecond\nThird"},
		{"code and lists", "  if a < b {\n\tprint(\"a  b\")\n  }\n\n- One\n  - Two", "  if a < b {\n\tprint(\"a  b\")\n  }\n\n- One\n  - Two"},
		{"not HTML", "Contact <person@example.com>\nUse <div> &amp; <https://example.com>", "Contact <person@example.com>\nUse <div> &amp; <https://example.com>"},
		{"no reflow or deduplication", "Repeat\nRepeat\n\nRepeat\n\nRepeat", "Repeat\nRepeat\n\nRepeat\n\nRepeat"},
		{"real joiners", "Person: 👩\u200d💻; word: a\u200cb", "Person: 👩\u200d💻; word: a\u200cb"},
		{"untrusted delimiters", "A\ufff0\ufff1\ufff2B", "AB"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizePlainText(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasEmailMarkupArtifacts(t *testing.T) {
	for _, value := range []string{
		"Notice\n<!--[if !mso]><!-->\nText", "<!--[IF false]><!-->",
		"<![if mso]>", "<![endif]-->", "<v:roundrect\n style=\"height:48px\">",
		"<w:anchorlock/>", "</v:textbox>", "<!DOCTYPE html>", "<HTML lang='en'>",
	} {
		if !HasEmailMarkupArtifacts(value) {
			t.Errorf("missed artifacts in %q", value)
		}
	}
	for _, value := range []string{
		"Contact <person@example.com>", "Use <div> and <p> in HTML.",
		"if x < y && y > z {}", "<https://example.com>", "<!-- example comment -->",
		"Your Google Account notice", "The word html", "",
	} {
		if HasEmailMarkupArtifacts(value) {
			t.Errorf("ordinary plain text marked as contaminated: %q", value)
		}
	}
}

func TestFormatExpandedMailPreservesBodyIndentation(t *testing.T) {
	body := "  first\n\tsecond  "
	got := FormatExpandedMail(models.ExpandedMail{BodyText: body})
	if !strings.HasSuffix(got, "Body:\n"+body) {
		t.Fatalf("body whitespace changed: %q", got)
	}
	if got := FormatExpandedMail(models.ExpandedMail{BodyText: " \t\n"}); !strings.Contains(got, "(no body text available)") {
		t.Fatal("missing empty body fallback")
	}
}
