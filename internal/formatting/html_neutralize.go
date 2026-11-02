package formatting

import (
	"strings"

	nethtml "golang.org/x/net/html"
)

// htmlRenamePrefix marks renamed formatting tags. <B id=1> becomes
// <span x-gmail-bot-B id=1>: like <b>, <span> breaks out of SVG/MathML content,
// and the original name becomes an ignored attribute. Keeping its spelling makes
// the rewrite reversible in text that the parser does not treat as markup (see
// restoreHTMLFormattingTags).
const htmlRenamePrefix = "x-gmail-bot-"

const htmlSpanRename = "span " + htmlRenamePrefix

// htmlAnchorTag is the parsed name of a renamed <a> (<A> becomes
// <x-gmail-bot-A>). Like <a>, it does not break out of foreign content. It is
// an ordinary element to the HTML5 parser; the text writer treats it as the
// anchor. Senders can also write it directly, which is harmless: it renders
// exactly like an <a> would.
const htmlAnchorTag = htmlRenamePrefix + "a"

// HTML5 closes an open anchor when another one starts. Emulate the common case
// by ending any anchor that is reachable without crossing a block element.
const htmlAnchorClose = "</" + htmlAnchorTag + ">"

// neutralizeHTMLFormattingTags renames HTML5 formatting elements (a, b, code,
// strong, ...) to ordinary elements before parsing. The parser reconstructs
// unclosed formatting elements on every later token and clones them during
// adoption-agency repair, so a small source can otherwise allocate a DOM
// quadratic in its size. Ordinary elements are never cloned, so the DOM stays
// linear in the source. The text writer does not depend on formatting.
//
// Rename at every '<' and '</', even inside comments, attributes and raw text.
// A standalone tokenizer does not know the DOM parser's foreign-content, select
// or noscript state and could miss tags the parser will see. The writer undoes
// the rename where it is visible: raw text and attribute values. Run this on the
// exact source passed to the parser, since removing conditionals can splice
// previously separate bytes into new tags.
func neutralizeHTMLFormattingTags(value string) string {
	var b strings.Builder
	last := 0
	for offset := 0; offset < len(value); {
		relative := strings.IndexByte(value[offset:], '<')
		if relative < 0 {
			break
		}
		start := offset + relative
		nameStart := start + 1
		endTag := nameStart < len(value) && value[nameStart] == '/'
		if endTag {
			nameStart++
		}
		offset = nameStart
		name := htmlFormattingTagName(value[nameStart:])
		if name == "" {
			continue
		}
		if b.Len() == 0 {
			b.Grow(len(value))
		}
		switch {
		case name != "a":
			b.WriteString(value[last:nameStart])
			b.WriteString(htmlSpanRename)
		case endTag:
			b.WriteString(value[last:nameStart])
			b.WriteString(htmlRenamePrefix)
		default:
			b.WriteString(value[last:start])
			b.WriteString(htmlAnchorClose)
			b.WriteByte('<')
			b.WriteString(htmlRenamePrefix)
		}
		last = nameStart
	}
	if last == 0 {
		return value
	}
	b.WriteString(value[last:])
	return b.String()
}

var htmlFormattingRestorer = strings.NewReplacer(
	htmlAnchorClose+"<"+htmlRenamePrefix, "<",
	"</"+htmlRenamePrefix, "</",
	"<"+htmlRenamePrefix, "<",
	"</"+htmlSpanRename, "</",
	"<"+htmlSpanRename, "<",
)

// restoreHTMLFormattingTags reverses neutralizeHTMLFormattingTags in text that
// the parser kept literally. Entity-decoded text that spells out the private
// prefix (&lt;span x-gmail-bot-b) is shown without it, which is harmless.
func restoreHTMLFormattingTags(value string) string {
	if !strings.Contains(value, htmlRenamePrefix) {
		return value
	}
	return htmlFormattingRestorer.Replace(value)
}

// The tokenizer keeps the contents of these HTML elements as text. The parser
// only ever cancels raw-text mode (foreign content, select, noscript), and then
// it creates no such HTML element, so their text children are always literal.
var htmlRawTextElements = map[string]bool{
	"iframe": true, "noembed": true, "noframes": true, "plaintext": true, "script": true,
	"style": true, "textarea": true, "title": true, "xmp": true,
}

func htmlTextNodeData(n *nethtml.Node) string {
	if p := n.Parent; p != nil && p.Type == nethtml.ElementNode && p.Namespace == "" && htmlRawTextElements[p.Data] {
		return restoreHTMLFormattingTags(n.Data)
	}
	return n.Data
}

// value starts immediately after '<' or '</'. Only ASCII HTML tag delimiters
// end a name, as in the tokenizer; inspecting at most seven bytes avoids
// rescanning long malformed names.
func htmlFormattingTagName(value string) string {
	end := 0
	for end < len(value) && end <= len("strong") && !strings.ContainsRune(" \t\r\n\f/>", rune(value[end])) {
		end++
	}
	if end == 0 || end > len("strong") {
		return ""
	}
	name := strings.ToLower(value[:end])
	switch name {
	case "a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u":
		return name
	}
	return ""
}
