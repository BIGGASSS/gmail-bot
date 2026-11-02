package formatting

import "strings"

// htmlAnchorTag replaces <a> before parsing. It is an ordinary element to the
// HTML5 parser; the text writer treats it as the anchor. Senders can also write
// it directly, which is harmless: it renders exactly like an <a> would.
const htmlAnchorTag = "x-gmail-bot-a"

// HTML5 closes an open anchor when another one starts. Emulate the common case
// by ending any anchor that is reachable without crossing a block element.
const htmlAnchorStart = "</" + htmlAnchorTag + "><" + htmlAnchorTag

// neutralizeHTMLFormattingTags renames HTML5 formatting elements (a, b, code,
// strong, ...) to ordinary elements before parsing. The parser reconstructs
// unclosed formatting elements on every later token and clones them during
// adoption-agency repair, so a small source can otherwise allocate a DOM
// quadratic in its size. Ordinary elements are never cloned, so the DOM stays
// linear in the source. The text writer does not depend on formatting.
//
// Rename at every '<' and '</', even inside comments, attributes and raw text.
// A standalone tokenizer does not know the DOM parser's foreign-content state
// and could miss tags the parser will see. Renaming text that is never parsed
// as a tag only affects invisible content or rare literal markup in attribute
// values. Run this on the exact source passed to the parser, since removing
// conditionals can splice previously separate bytes into new tags.
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
			b.WriteString("span")
		case endTag:
			b.WriteString(value[last:nameStart])
			b.WriteString(htmlAnchorTag)
		default:
			b.WriteString(value[last:start])
			b.WriteString(htmlAnchorStart)
		}
		last = nameStart + len(name)
		offset = last
	}
	if last == 0 {
		return value
	}
	b.WriteString(value[last:])
	return b.String()
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
