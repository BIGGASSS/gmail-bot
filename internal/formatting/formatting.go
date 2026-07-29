package formatting

import (
	"encoding/base64"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/BIGGASSS/gmail-bot/internal/models"
	nethtml "golang.org/x/net/html"
)

const (
	TelegramMessageLimit = 4096
	SafeMessageChunk     = 3500
	// SafeByteLimit is a byte budget for rendered HTML. Telegram's limit is
	// 4096 UTF-16 code units; every UTF-8 byte corresponds to at least one
	// UTF-16 code unit, so 3900 bytes is safely under the limit.
	SafeByteLimit      = 3900
	linkTokenStart     = "\ufff0"
	linkTokenSeparator = "\ufff1"
	linkTokenEnd       = "\ufff2"
)

var (
	urlPattern       = regexp.MustCompile(`<(?P<bracketed>https?://[^<>\s]+)>|(?P<plain>https?://[^\s<>]+)`)
	linkTokenPattern = regexp.MustCompile(
		linkTokenStart + `(?P<label>[A-Za-z0-9_-]+)` + linkTokenSeparator + `(?P<url>[A-Za-z0-9_-]+)` + linkTokenEnd,
	)
	blockTags = map[string]struct{}{
		"address": {}, "article": {}, "aside": {}, "blockquote": {}, "br": {},
		"div": {}, "dd": {}, "dl": {}, "dt": {}, "figcaption": {}, "figure": {},
		"footer": {}, "h1": {}, "h2": {}, "h3": {}, "h4": {}, "h5": {}, "h6": {},
		"header": {}, "hr": {}, "li": {}, "main": {}, "ol": {}, "p": {}, "pre": {},
		"section": {}, "table": {}, "td": {}, "th": {}, "tr": {}, "ul": {},
	}
)

// SanitizeLinkTokenDelimiters strips the private-use Unicode delimiters
// used as internal link tokens from untrusted text, preventing forged
// Telegram anchor tags.
func SanitizeLinkTokenDelimiters(value string) string {
	value = strings.ReplaceAll(value, linkTokenStart, "")
	value = strings.ReplaceAll(value, linkTokenSeparator, "")
	value = strings.ReplaceAll(value, linkTokenEnd, "")
	return value
}

func NormalizeWhitespace(value string) string {
	rawLines := strings.Split(value, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		collapsed := strings.Join(strings.Fields(line), " ")
		if collapsed != "" {
			lines = append(lines, collapsed)
		}
	}
	return strings.Join(lines, "\n")
}

func NormalizeGmailSnippet(value string) string {
	return NormalizeWhitespace(SanitizeLinkTokenDelimiters(html.UnescapeString(value)))
}

func encodeLinkComponent(value string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(value))
	return encoded
}

func decodeLinkComponent(value string) (string, error) {
	// Accept both padded and unpadded inputs.
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(value)
		if err != nil {
			// Add padding if needed.
			padding := (4 - len(value)%4) % 4
			padded := value + strings.Repeat("=", padding)
			decoded, err = base64.URLEncoding.DecodeString(padded)
			if err != nil {
				return "", err
			}
		}
	}
	return string(decoded), nil
}

func EncodeLinkToken(label, url string) string {
	return linkTokenStart + encodeLinkComponent(label) + linkTokenSeparator + encodeLinkComponent(url) + linkTokenEnd
}

func renderLink(label, url string) string {
	safeURL := html.EscapeString(url)
	// html.EscapeString escapes quotes as &#34; which is fine for attributes.
	// Python's escape(url, quote=True) produces &quot; for quotes. Both work in HTML.
	// For amp in URLs, both escape & to &amp;.
	safeLabel := html.EscapeString(label)
	return fmt.Sprintf(`<a href="%s">%s</a>`, safeURL, safeLabel)
}

func renderPlainSegment(value string) string {
	var b strings.Builder
	last := 0
	for _, match := range urlPattern.FindAllStringSubmatchIndex(value, -1) {
		b.WriteString(html.EscapeString(value[last:match[0]]))
		url := ""
		// groups: full, bracketed, plain
		if match[2] >= 0 {
			url = value[match[2]:match[3]]
		} else if match[4] >= 0 {
			url = value[match[4]:match[5]]
		}
		b.WriteString(renderLink(url, url))
		last = match[1]
	}
	b.WriteString(html.EscapeString(value[last:]))
	return b.String()
}

func RenderTelegramHTML(value string) string {
	var b strings.Builder
	last := 0
	for _, match := range linkTokenPattern.FindAllStringSubmatchIndex(value, -1) {
		b.WriteString(renderPlainSegment(value[last:match[0]]))
		labelEnc := value[match[2]:match[3]]
		urlEnc := value[match[4]:match[5]]
		label, err := decodeLinkComponent(labelEnc)
		if err != nil {
			label = labelEnc
		}
		url, err := decodeLinkComponent(urlEnc)
		if err != nil {
			url = urlEnc
		}
		b.WriteString(renderLink(label, url))
		last = match[1]
	}
	b.WriteString(renderPlainSegment(value[last:]))
	return b.String()
}

func StripHTML(value string) string {
	return NormalizeWhitespace(extractHTMLText(value, false))
}

func HTMLToTelegramText(value string) string {
	return NormalizeWhitespace(extractHTMLText(value, true))
}

func extractHTMLText(value string, preserveAnchorTextLinks bool) string {
	tokenizer := nethtml.NewTokenizer(strings.NewReader(value))
	parts := make([]string, 0, 32)
	type anchorFrame struct {
		href       string
		startIndex int
	}
	var anchors []anchorFrame

	handleStart := func(tag string, attrs []nethtml.Attribute) {
		if _, ok := blockTags[tag]; ok {
			parts = append(parts, "\n")
		}
		if tag == "a" {
			href := ""
			for _, attr := range attrs {
				if attr.Key == "href" {
					href = strings.TrimSpace(attr.Val)
					break
				}
			}
			anchors = append(anchors, anchorFrame{href: href, startIndex: len(parts)})
		}
	}

	handleEnd := func(tag string) {
		if tag == "a" && len(anchors) > 0 {
			frame := anchors[len(anchors)-1]
			anchors = anchors[:len(anchors)-1]
			if frame.href != "" {
				linkText := strings.TrimSpace(strings.Join(parts[frame.startIndex:], ""))
				if preserveAnchorTextLinks {
					label := linkText
					if label == "" {
						label = frame.href
					}
					parts = append(parts[:frame.startIndex], EncodeLinkToken(label, frame.href))
				} else if linkText == "" {
					parts = append(parts, frame.href)
				} else if linkText != frame.href {
					parts = append(parts, " <"+frame.href+">")
				}
			}
		}
		if _, ok := blockTags[tag]; ok {
			parts = append(parts, "\n")
		}
	}

	for {
		tt := tokenizer.Next()
		switch tt {
		case nethtml.ErrorToken:
			return strings.Join(parts, "")
		case nethtml.TextToken:
			parts = append(parts, SanitizeLinkTokenDelimiters(string(tokenizer.Text())))
		case nethtml.StartTagToken:
			nameBytes, hasAttr := tokenizer.TagName()
			tag := string(nameBytes)
			attrs := collectAttrs(tokenizer, hasAttr)
			handleStart(tag, attrs)
		case nethtml.SelfClosingTagToken:
			nameBytes, hasAttr := tokenizer.TagName()
			tag := string(nameBytes)
			attrs := collectAttrs(tokenizer, hasAttr)
			handleStart(tag, attrs)
			if tag == "a" {
				handleEnd(tag)
			}
		case nethtml.EndTagToken:
			nameBytes, _ := tokenizer.TagName()
			handleEnd(string(nameBytes))
		}
	}
}

func collectAttrs(tokenizer *nethtml.Tokenizer, hasAttr bool) []nethtml.Attribute {
	if !hasAttr {
		return nil
	}
	var attrs []nethtml.Attribute
	for {
		key, val, more := tokenizer.TagAttr()
		attrs = append(attrs, nethtml.Attribute{Key: string(key), Val: string(val)})
		if !more {
			break
		}
	}
	return attrs
}

func FormatTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05 UTC")
}

func FormatMailNotification(mail models.IncomingMail) string {
	from := mail.FromHeader
	if from == "" {
		from = "Unknown sender"
	}
	from = SanitizeLinkTokenDelimiters(from)
	subject := mail.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	subject = SanitizeLinkTokenDelimiters(subject)
	snippet := mail.Snippet
	if snippet == "" {
		snippet = "(no preview available)"
	}
	return strings.Join([]string{
		"New Gmail message",
		"From: " + from,
		"Subject: " + subject,
		"Received: " + FormatTimestamp(mail.ReceivedAt),
		"",
		"Snippet: " + snippet,
	}, "\n")
}

func FormatExpandedMail(mail models.ExpandedMail) string {
	bodyText := strings.TrimSpace(mail.BodyText)
	if bodyText == "" {
		bodyText = "(no body text available)"
	}
	from := mail.FromHeader
	if from == "" {
		from = "Unknown sender"
	}
	from = SanitizeLinkTokenDelimiters(from)
	subject := mail.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	subject = SanitizeLinkTokenDelimiters(subject)
	lines := []string{
		"Expanded Gmail message",
		"From: " + from,
		"Subject: " + subject,
		"Received: " + FormatTimestamp(mail.ReceivedAt),
		"",
		"Body:",
		bodyText,
	}
	if len(mail.Attachments) > 0 {
		lines = append(lines, "", "Attachments:")
		lines = append(lines, FormatAttachmentLines(mail.Attachments)...)
	}
	return strings.Join(lines, "\n")
}

func FormatAttachmentLines(attachments []models.AttachmentMeta) []string {
	lines := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		sizeSuffix := "size unknown"
		if attachment.Size != 0 {
			sizeSuffix = fmt.Sprintf("%d bytes", attachment.Size)
		}
		name := attachment.Filename
		if name == "" {
			name = "(unnamed attachment)"
		}
		mimeType := attachment.MimeType
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		lines = append(lines, fmt.Sprintf("- %s [%s, %s]", name, mimeType, sizeSuffix))
	}
	return lines
}

// safeSplitAtRunes adjusts splitAt so it does not fall inside a link token
// within runes[start:splitAt]. If the split lands inside a token, the split
// is moved to just before the token start.
func safeSplitAtRunes(runes []rune, start, splitAt int) int {
	if splitAt > len(runes) {
		splitAt = len(runes)
	}
	if splitAt <= start {
		return splitAt
	}
	// Check if we're inside a link token by searching backwards for linkTokenStart.
	prefix := string(runes[start:splitAt])
	tokenStart := strings.LastIndex(prefix, linkTokenStart)
	if tokenStart == -1 {
		return splitAt
	}
	tokenEnd := strings.LastIndex(prefix, linkTokenEnd)
	if tokenEnd > tokenStart {
		return splitAt
	}
	// Inside a link token — move split to before the token start.
	startRune := len([]rune(prefix[:tokenStart]))
	if startRune == 0 {
		return splitAt
	}
	return start + startRune
}

func ChunkText(value string, limit int) []string {
	if limit <= 0 {
		limit = SafeMessageChunk
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return []string{value}
	}

	var chunks []string
	start := 0
	for start < len(runes) {
		if len(runes)-start <= limit {
			chunks = append(chunks, string(runes[start:]))
			break
		}

		end := start + limit
		// Find last newline in runes[start:end].
		splitAt := end
		for i := end - 1; i >= start; i-- {
			if runes[i] == '\n' {
				splitAt = i
				break
			}
		}
		if splitAt <= start {
			splitAt = end
		}
		// Avoid splitting inside a link token.
		splitAt = safeSplitAtRunes(runes, start, splitAt)
		// Trim trailing whitespace from the chunk.
		chunkEnd := splitAt
		for chunkEnd > start && unicode.IsSpace(runes[chunkEnd-1]) {
			chunkEnd--
		}
		if chunkEnd > start {
			chunks = append(chunks, string(runes[start:chunkEnd]))
		}
		// Skip leading newlines of the next chunk.
		start = splitAt
		for start < len(runes) && runes[start] == '\n' {
			start++
		}
	}
	return chunks
}

// RenderAndChunk renders value to Telegram HTML and splits the result into
// chunks that each fit within byteLimit bytes. The returned chunks are
// already rendered HTML — callers must NOT call RenderTelegramHTML again.
func RenderAndChunk(value string, byteLimit int) []string {
	if byteLimit <= 0 {
		byteLimit = SafeByteLimit
	}
	rendered := RenderTelegramHTML(value)
	if len(rendered) <= byteLimit {
		return []string{rendered}
	}
	return renderAndChunk(value, byteLimit, SafeMessageChunk)
}

func renderAndChunk(value string, byteLimit, runeLimit int) []string {
	rendered := RenderTelegramHTML(value)
	if len(rendered) <= byteLimit {
		return []string{rendered}
	}
	if runeLimit <= 1 {
		// Can't split further; a single rune renders to at most a few bytes,
		// but an unsplittable link token may exceed the limit. Best effort.
		return []string{rendered}
	}
	chunks := ChunkText(value, runeLimit)
	if len(chunks) <= 1 {
		// Couldn't split at this limit; retry with a smaller one.
		return renderAndChunk(value, byteLimit, runeLimit/2)
	}
	result := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		result = append(result, renderAndChunk(chunk, byteLimit, runeLimit)...)
	}
	return result
}
