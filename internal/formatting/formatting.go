package formatting

import (
	"encoding/base64"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
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
	// 4096 UTF-16 code units. Valid UTF-8 text uses at least as many bytes
	// as UTF-16 code units, so 3900 rendered bytes is a conservative limit.
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
	// Structural boundaries, in newlines. Layout containers only need a line;
	// paragraphs and headings deserve a blank line. Cells are handled per row.
	blockTags = map[string]int{
		"address": 1, "article": 2, "aside": 2, "blockquote": 2,
		"div": 1, "dd": 1, "dl": 1, "dt": 1, "figcaption": 1, "figure": 2,
		"footer": 2, "h1": 2, "h2": 2, "h3": 2, "h4": 2, "h5": 2, "h6": 2,
		"header": 2, "main": 2, "p": 2, "section": 2, "table": 1,
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
	return extractHTMLText(value, true)
}

func extractHTMLText(value string, preserveAnchorTextLinks bool) string {
	// Filter paired, downlevel-revealed Outlook branches before parsing: HTML
	// error recovery can move their comment markers outside tables or the body.
	// Raw bytes are retained, so the DOM parser alone decodes entities, once.
	doc, err := nethtml.ParseWithOptions(strings.NewReader(filterHTMLConditionals(value)), nethtml.ParseOptionEnableScripting(false))
	if err != nil {
		return ""
	}
	w := htmlTextWriter{links: preserveAnchorTextLinks}
	w.walk(doc, false)
	return w.out.String()
}

// Bound fragment copying and indentation on pathological mail. Beyond these
// limits, an iterative traversal retains text without complex layout formatting.
const (
	maxHTMLListDepth   = 8
	maxHTMLRenderDepth = 64
)

// htmlTextWriter defers whitespace until visible content arrives. This ignores
// source indentation and empty layout/spacer elements without deduplicating text.
// Preformatted text bypasses whitespace folding (including its blank lines).
// leading* also preserve the boundaries of fragments used as anchor labels.
type htmlTextWriter struct {
	out          strings.Builder
	links        bool
	inAnchor     bool
	space        bool
	breaks       int
	leadingSpace bool
	leadingBreak int
	lists        []htmlList
	depth        int
	invisible    bool
}

type htmlList struct {
	ordered bool
	next    int
	step    int
}

func (w *htmlTextWriter) boundary(lines int) {
	if lines > w.breaks {
		w.breaks = lines
	}
	w.space = false
}

func (w *htmlTextWriter) append(value string) {
	if value == "" {
		return
	}
	if w.out.Len() == 0 {
		w.leadingSpace, w.leadingBreak = w.space, w.breaks
	} else if w.breaks > 0 {
		// A preformatted fragment may already end in a newline.
		s := w.out.String()
		existing := len(s) - len(strings.TrimRight(s, "\n"))
		if w.breaks > existing {
			w.out.WriteString(strings.Repeat("\n", w.breaks-existing))
		}
	} else if w.space && !strings.HasSuffix(w.out.String(), "\n") {
		w.out.WriteByte(' ')
	}
	w.space, w.breaks = false, 0
	w.out.WriteString(value)
}

func (w *htmlTextWriter) text(value string, pre bool) {
	value = SanitizeLinkTokenDelimiters(value)
	if value == "" {
		return
	}
	if pre {
		w.append(value)
		return
	}
	// Email spacers sometimes contain only invisible padding, not just NBSP.
	// Keep these characters inside real words/emoji; only discard filler nodes.
	if strings.TrimFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("\u200b\u200c\u200d\ufeff\u00ad", r)
	}) == "" {
		w.space = true
		return
	}
	start := 0
	for i, r := range value {
		if !unicode.IsSpace(r) {
			continue
		}
		w.append(value[start:i])
		w.space = true
		start = i + len(string(r))
	}
	w.append(value[start:])
}

func (w *htmlTextWriter) children(n *nethtml.Node, pre bool) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, pre)
	}
}

func (w *htmlTextWriter) fragment(n *nethtml.Node, pre bool, childrenOnly bool) *htmlTextWriter {
	fragment := w.newFragment()
	if childrenOnly {
		fragment.children(n, pre)
	} else {
		fragment.walk(n, pre)
	}
	return fragment
}

func (w *htmlTextWriter) newFragment() *htmlTextWriter {
	return &htmlTextWriter{
		links: w.links, inAnchor: w.inAnchor, depth: w.depth, invisible: w.invisible,
		// The list stack never exceeds maxHTMLListDepth.
		lists: append([]htmlList(nil), w.lists...),
	}
}

func (w *htmlTextWriter) walk(n *nethtml.Node, pre bool) {
	if w.depth >= maxHTMLRenderDepth {
		w.flatten(n, pre)
		return
	}
	w.depth++
	defer func() { w.depth-- }()
	switch n.Type {
	case nethtml.TextNode:
		if !w.invisible {
			w.text(n.Data, pre)
		}
		return
	case nethtml.DocumentNode:
		w.children(n, pre)
		return
	case nethtml.ElementNode:
		prune, invisible := htmlElementVisibility(n, w.invisible)
		if prune {
			return
		}
		inherited := w.invisible
		w.invisible = invisible
		defer func() { w.invisible = inherited }()
	default: // Comments and doctypes never contribute visible text.
		return
	}

	switch n.Data {
	case "br":
		if w.invisible {
			return
		}
		if pre {
			w.append("\n")
		} else if w.breaks < 2 {
			w.boundary(w.breaks + 1)
		}
	case "hr":
		if !w.invisible {
			w.boundary(2)
		}
	case "pre":
		w.boundary(2)
		w.children(n, true)
		w.boundary(2)
	case "a":
		// Foreign/malformed markup can retain nested anchors in the DOM. Never
		// encode a token inside another token's label.
		if w.inAnchor {
			w.children(n, pre)
			return
		}
		label := w.newFragment()
		label.inAnchor = true
		label.children(n, pre)
		text := label.out.String()
		href, _ := htmlAttribute(n, "href")
		href = safeHTMLLink(href)
		if href != "" && (text != "" || !w.invisible) {
			if text == "" {
				text = href
			}
			if w.links {
				text = EncodeLinkToken(text, href)
			} else if text != href {
				text += " <" + href + ">"
			}
		}
		if label.leadingBreak > 0 {
			w.boundary(label.leadingBreak)
		}
		w.space = w.space || label.leadingSpace
		w.append(text)
		if label.breaks > 0 {
			w.boundary(label.breaks)
		}
		w.space = w.space || label.space
	case "ul", "ol":
		if len(w.lists) >= maxHTMLListDepth {
			w.flatten(n, pre)
			return
		}
		list := htmlList{ordered: n.Data == "ol", next: 1, step: 1}
		if _, reversed := htmlAttribute(n, "reversed"); reversed && list.ordered {
			list.next, list.step = 0, -1
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == nethtml.ElementNode && c.Data == "li" {
					list.next++
				}
			}
		}
		if start, ok := htmlIntegerAttribute(n, "start"); ok {
			list.next = start
		}
		w.boundary(1)
		w.lists = append(w.lists, list)
		w.children(n, pre)
		w.lists = w.lists[:len(w.lists)-1]
		w.boundary(1)
	case "li":
		prefix := "- "
		if len(w.lists) > 0 {
			list := &w.lists[len(w.lists)-1]
			if list.ordered {
				if value, ok := htmlIntegerAttribute(n, "value"); ok {
					list.next = value
				}
				prefix = strconv.Itoa(list.next) + ". "
				list.next += list.step
			}
		}
		if w.invisible {
			prefix = "" // A visible descendant does not reveal the item's marker.
		}
		text := w.fragment(n, pre, true).out.String()
		if text != "" {
			lines := strings.Split(text, "\n")
			for i := 1; i < len(lines); i++ {
				if lines[i] != "" {
					lines[i] = strings.Repeat(" ", len(prefix)) + lines[i]
				}
			}
			w.boundary(1)
			w.append(prefix + strings.Join(lines, "\n"))
			w.boundary(1)
		}
	case "tr":
		w.boundary(1)
		var cells []string
		hasText := false
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == nethtml.ElementNode {
				if prune, _ := htmlElementVisibility(c, w.invisible); prune {
					continue
				}
			}
			text := w.fragment(c, pre, false).out.String()
			if text != "" || (c.Type == nethtml.ElementNode && (c.Data == "td" || c.Data == "th")) {
				cells = append(cells, text)
				hasText = hasText || text != ""
			}
		}
		if hasText {
			w.append(strings.Join(cells, " | "))
		}
		w.boundary(1)
	default:
		if lines := blockTags[n.Data]; lines > 0 {
			w.boundary(lines)
			w.children(n, pre)
			w.boundary(lines)
		} else {
			w.children(n, pre)
		}
	}
}

// flatten visits each remaining node once, without recursive fragments or
// indentation. Visibility still inherits and can be overridden by descendants.
func (w *htmlTextWriter) flatten(n *nethtml.Node, pre bool) {
	type visit struct {
		node      *nethtml.Node
		pre       bool
		invisible bool
		boundary  int
	}
	pending := []visit{{node: n, pre: pre, invisible: w.invisible}}
	for len(pending) > 0 {
		v := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if v.node == nil {
			w.boundary(v.boundary)
			continue
		}
		n := v.node
		lines := 0
		switch n.Type {
		case nethtml.TextNode:
			if !v.invisible {
				w.text(n.Data, v.pre)
			}
			continue
		case nethtml.ElementNode:
			prune, invisible := htmlElementVisibility(n, v.invisible)
			if prune {
				continue
			}
			v.invisible = invisible
			switch n.Data {
			case "br":
				if !v.invisible {
					if v.pre {
						w.append("\n")
					} else if w.breaks < 2 {
						w.boundary(w.breaks + 1)
					}
				}
				continue
			case "hr":
				if !v.invisible {
					w.boundary(2)
				}
				continue
			case "pre":
				v.pre, lines = true, 2
			case "ul", "ol", "li", "tr", "td", "th":
				lines = 1
			default:
				lines = blockTags[n.Data]
			}
		case nethtml.DocumentNode:
		default:
			continue
		}
		if lines > 0 {
			w.boundary(lines)
			pending = append(pending, visit{boundary: lines})
		}
		for c := n.LastChild; c != nil; c = c.PrevSibling {
			pending = append(pending, visit{node: c, pre: v.pre, invisible: v.invisible})
		}
	}
}

func htmlAttribute(n *nethtml.Node, key string) (string, bool) {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val, true
		}
	}
	return "", false
}

func htmlIntegerAttribute(n *nethtml.Node, key string) (int, bool) {
	value, exists := htmlAttribute(n, key)
	number, err := strconv.Atoi(strings.TrimSpace(value))
	return number, exists && err == nil
}

var htmlCSSComments = regexp.MustCompile(`(?s)/\*.*?\*/`)

// display:none and hidden prune subtrees; visibility is inherited but a visible
// descendant can override it. Office text wrappers are transparent, unlike VML.
func htmlElementVisibility(n *nethtml.Node, inherited bool) (prune, invisible bool) {
	switch n.Data {
	case "head", "title", "script", "style", "template", "xml":
		return true, inherited
	}
	if strings.HasPrefix(n.Data, "v:") {
		return true, inherited
	}
	if _, hidden := htmlAttribute(n, "hidden"); hidden {
		return true, inherited
	}
	style, _ := htmlAttribute(n, "style")
	if style == "" {
		return false, inherited
	}
	style = htmlCSSComments.ReplaceAllString(strings.ToLower(style), "")
	values := make(map[string]string)
	important := make(map[string]bool)
	for _, declaration := range strings.Split(style, ";") {
		parts := strings.SplitN(declaration, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if key != "display" && key != "visibility" {
			continue
		}
		isImportant := false
		if bang := strings.LastIndex(value, "!"); bang >= 0 && strings.TrimSpace(value[bang+1:]) == "important" {
			isImportant = true
			value = strings.TrimSpace(value[:bang])
		}
		if !important[key] || isImportant {
			values[key], important[key] = value, isImportant
		}
	}
	invisible = inherited
	switch values["visibility"] {
	case "hidden", "collapse":
		invisible = true
	case "visible", "initial":
		invisible = false
	}
	return values["display"] == "none", invisible
}

// There is no trusted base URL for relative links in received email. Only emit
// explicit web/mail links; reject controls and delimiters rather than repairing
// an attacker-supplied scheme. Attributes have already been entity-decoded.
func safeHTMLLink(value string) string {
	value = strings.TrimSpace(value)
	if strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune(linkTokenStart+linkTokenSeparator+linkTokenEnd, r)
	}) >= 0 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.Hostname() != "" {
			return value
		}
	case "mailto":
		if parsed.Opaque != "" {
			return value
		}
	}
	return ""
}

// Only paired markers can suppress content. An unmatched/malformed revealed
// opener must not swallow the rest of a real message. Hidden conditional
// comments remain comments, while !mso and unknown conditions fail open.
func filterHTMLConditionals(value string) string {
	type frame struct {
		start int
		skip  bool
	}
	var stack []frame
	out := make([]byte, 0, len(value))
	tokenizer := nethtml.NewTokenizer(strings.NewReader(value))
	for {
		kind := tokenizer.Next()
		start := len(out)
		out = append(out, tokenizer.Raw()...)
		if kind == nethtml.ErrorToken {
			return string(out)
		}
		if kind == nethtml.StartTagToken {
			name, _ := tokenizer.TagName()
			if string(name) == "noscript" {
				// Match the DOM parser's disabled-scripting behavior.
				tokenizer.NextIsNotRawText()
			}
		}
		if kind != nethtml.CommentToken {
			continue
		}
		data := strings.ToLower(strings.TrimSpace(tokenizer.Token().Data))
		end := strings.TrimSpace(strings.TrimPrefix(data, "<!"))
		if strings.TrimRight(end, "-> \t\r\n") == "[endif]" {
			if len(stack) > 0 {
				open := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if open.skip {
					out = out[:open.start]
				}
			}
			continue
		}
		if !strings.HasPrefix(data, "[if") {
			continue
		}
		close := strings.IndexByte(data, ']')
		if close < 4 {
			continue
		}
		fields := strings.Fields(data[1:close])
		if len(fields) < 2 || fields[0] != "if" {
			continue
		}
		tail := strings.TrimSpace(data[close+1:])
		if tail != "" && tail != ">" && tail != "><!" {
			continue // A whole hidden conditional is a single comment, not a pair.
		}
		visible, known := htmlConditionalValue(data[3:close])
		stack = append(stack, frame{start: start, skip: known && !visible})
	}
}

var htmlOutlookCondition = regexp.MustCompile(`^(?:(?:lt|lte|gt|gte) )?(?:mso|ie)(?: [0-9.]+)?$`)

// Evaluate the small boolean language used by Outlook conditional comments for
// a non-Outlook client. Unknown syntax stays visible rather than losing mail.
func htmlConditionalValue(condition string) (value, known bool) {
	// Conditions in real email are tiny. Bound recursive work on untrusted input.
	if len(condition) > 256 {
		return false, false
	}
	condition = strings.Join(strings.Fields(condition), " ")
	for _, operator := range []byte{'|', '&'} {
		depth := 0
		for i := 0; i < len(condition); i++ {
			switch condition[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 && condition[i] == operator {
				left, lk := htmlConditionalValue(condition[:i])
				right, rk := htmlConditionalValue(condition[i+1:])
				if operator == '|' {
					return left || right, (lk && rk) || (lk && left) || (rk && right)
				}
				return left && right, (lk && rk) || (lk && !left) || (rk && !right)
			}
		}
	}
	if strings.HasPrefix(condition, "(") && strings.HasSuffix(condition, ")") {
		return htmlConditionalValue(condition[1 : len(condition)-1])
	}
	if strings.HasPrefix(condition, "!") {
		value, known := htmlConditionalValue(condition[1:])
		return !value, known
	}
	if condition == "true" {
		return true, true
	}
	if condition == "false" || htmlOutlookCondition.MatchString(condition) {
		return false, true
	}
	return false, false
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
	bodyText := mail.BodyText
	if strings.TrimSpace(bodyText) == "" {
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
		lines = append(lines, fmt.Sprintf("- %s [%s, %s]",
			SanitizeLinkTokenDelimiters(name), SanitizeLinkTokenDelimiters(mimeType), sizeSuffix))
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

// TruncateText caps the internal representation at limit runes without cutting
// an encoded link token. A link crossing the cutoff falls back to decoded text.
func TruncateText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	cutoff := len(string(runes[:limit]))
	for _, match := range linkTokenPattern.FindAllStringSubmatchIndex(value, -1) {
		if match[0] >= cutoff {
			break
		}
		if match[1] > cutoff {
			label, _ := decodeLinkComponent(value[match[2]:match[3]])
			url, _ := decodeLinkComponent(value[match[4]:match[5]])
			plain := []rune(SanitizeLinkTokenDelimiters(linkPlainText(label, url)))
			remaining := limit - len([]rune(value[:match[0]]))
			if len(plain) > remaining {
				plain = plain[:remaining]
			}
			return value[:match[0]] + string(plain)
		}
	}
	return string(runes[:limit])
}

func linkPlainText(label, url string) string {
	if label == url {
		return label
	}
	return label + " <" + url + ">"
}

// RenderAndChunk renders complete tokens before splitting decoded text. Long
// labels repeat the original target on each fragment; targets too large for an
// anchor fall back to plain label/URL text. HTML entities and UTF-8 runes remain
// intact. Limits below 6 are raised to 6 (the longest escaped single rune).
// Callers must NOT call RenderTelegramHTML on the returned chunks again.
func RenderAndChunk(value string, byteLimit int) []string {
	if byteLimit <= 0 {
		byteLimit = SafeByteLimit
	}
	if byteLimit < 6 {
		byteLimit = 6
	}
	var chunks []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
	}
	appendHTML := func(fragment string) {
		if current.Len()+len(fragment) > byteLimit {
			flush()
		}
		current.WriteString(fragment)
	}
	appendText := func(text string) {
		for _, r := range text {
			appendHTML(html.EscapeString(string(r)))
		}
	}
	appendLink := func(label, url string) {
		rendered := renderLink(label, url)
		if len(rendered) <= byteLimit {
			appendHTML(rendered)
			return
		}
		prefix := `<a href="` + html.EscapeString(url) + `">`
		const suffix = "</a>"
		budget := byteLimit - len(prefix) - len(suffix)
		// Ensure every escaped rune fits before emitting any fragments.
		for _, r := range label {
			if len(html.EscapeString(string(r))) > budget {
				appendText(linkPlainText(label, url))
				return
			}
		}
		if budget <= 0 {
			appendText(linkPlainText(label, url))
			return
		}
		var part strings.Builder
		for _, r := range label {
			escaped := html.EscapeString(string(r))
			if part.Len()+len(escaped) > budget {
				appendHTML(prefix + part.String() + suffix)
				part.Reset()
			}
			part.WriteString(escaped)
		}
		if part.Len() > 0 {
			appendHTML(prefix + part.String() + suffix)
		}
	}
	appendPlain := func(text string) {
		last := 0
		for _, match := range urlPattern.FindAllStringSubmatchIndex(text, -1) {
			appendText(text[last:match[0]])
			start, end := match[2], match[3]
			if start < 0 {
				start, end = match[4], match[5]
			}
			url := text[start:end]
			appendLink(url, url)
			last = match[1]
		}
		appendText(text[last:])
	}
	last := 0
	for _, match := range linkTokenPattern.FindAllStringSubmatchIndex(value, -1) {
		appendPlain(value[last:match[0]])
		label, labelErr := decodeLinkComponent(value[match[2]:match[3]])
		url, urlErr := decodeLinkComponent(value[match[4]:match[5]])
		if labelErr != nil || urlErr != nil {
			appendText(SanitizeLinkTokenDelimiters(value[match[0]:match[1]]))
		} else {
			appendLink(label, url)
		}
		last = match[1]
	}
	appendPlain(value[last:])
	flush()
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}
