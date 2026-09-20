package formatting

import (
	"regexp"
	"strings"
	"unicode"
)

// HasEmailMarkupArtifacts recognizes strong signs of a broken plain-text
// alternative. Ordinary angle brackets, email addresses, and code are not HTML.
// This is only a selection hint: never parse arbitrary plain text as HTML.
func HasEmailMarkupArtifacts(value string) bool {
	return emailMarkupArtifacts.MatchString(value)
}

var emailMarkupArtifacts = regexp.MustCompile(`(?i)<!(?:--\s*)?\[(?:if\b|endif\b)|</?(?:v|o|w):[a-z][a-z0-9]*\b|<!doctype\s+html\b|<html(?:\s|>)`)

// NormalizePlainText removes empty padding and excessive blank lines without
// reflowing prose or destroying indentation, lists, code, or literal markup.
// Entities in text/plain are intentionally not HTML-decoded.
func NormalizePlainText(value string) string {
	value = SanitizeLinkTokenDelimiters(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	var out strings.Builder
	blank := false
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimFunc(line, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune("\u200b\u200c\u200d\ufeff\u00ad", r)
		}) == "" {
			blank = true
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
			if blank {
				out.WriteByte('\n')
			}
		}
		out.WriteString(line)
		blank = false
	}
	return out.String()
}
