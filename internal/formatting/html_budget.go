package formatting

import (
	"io"
	"strings"

	nethtml "golang.org/x/net/html"
)

const (
	maxHTMLCloneBytes         = 64 << 20
	maxHTMLFormattingTagBytes = 8 << 10
	maxHTMLBudgetScanBytes    = 128 << 10
)

// checkHTMLCloneBudget bounds HTML5 formatting-node reconstruction BEFORE DOM
// allocation. A small source can otherwise create millions of nodes. Count all
// possible formatting starts, not just apparently unclosed tags: error recovery
// can keep formatting active after its source-level ancestors have closed.
//
// Inspect every '<' independently, even inside comments, attributes and raw text.
// A standalone tokenizer does not know the DOM parser's foreign-content state
// and can hide tags that the parser will reconstruct. False positives here are
// intentional. Run this on the exact source passed to the parser, since removing
// conditionals can splice previously separate bytes into new tags.
func checkHTMLCloneBudget(value string) error {
	var starts []int
	var markup, nobr, repairs int64
	for offset := 0; offset < len(value); {
		relative := strings.IndexByte(value[offset:], '<')
		if relative < 0 {
			break
		}
		start := offset + relative
		offset = start + 1 // Do not skip '<' inside this candidate's token.
		markup++
		source := value[offset:]
		endTag := strings.HasPrefix(source, "/")
		if endTag {
			source = source[1:]
		}
		name := htmlFormattingTagName(source)
		if name == "" {
			continue
		}
		if endTag || name == "a" || name == "nobr" {
			repairs++
		}
		if !endTag {
			starts = append(starts, start)
			if name == "nobr" {
				nobr++
			}
		}
	}

	// Clones share strings, but copy the node and its attribute slice. These
	// weights include allocation rounding on 64-bit platforms. Charge nodes
	// before any candidate tokenization, including incomplete tags at EOF.
	totalWeight := int64(len(starts)) * 128
	largestWeight := int64(128)
	exceedsBudget := func() bool {
		// In x/net/html, each token can reconstruct the entire formatting
		// list; <nobr> can do so twice. There are at most 2*markup+2 tokens.
		// Adoption-agency repair can clone up to four formatting nodes per
		// iteration, for at most eight iterations per relevant tag. Overcount
		// both paths without trying to emulate parser state. Keep these
		// assumptions in sync with x/net/html when upgrading it.
		repairWeight := 4 * largestWeight
		if repairWeight > totalWeight {
			repairWeight = totalWeight
		}
		cloneBytes := (2*markup+2+nobr)*totalWeight + 8*repairs*repairWeight
		return cloneBytes > maxHTMLCloneBytes
	}
	if exceedsBudget() {
		return ErrHTMLResourceLimit
	}

	scanned := 0
	for _, start := range starts {
		// Tokenize just this candidate to count even duplicate/boolean or
		// malformed attributes as net/html does, without allocating a Node or
		// copying attribute strings. Bound both individual and cumulative
		// scans: overlapping, incomplete tags must not amplify preflight work.
		z := nethtml.NewTokenizer(strings.NewReader(value[start:]))
		z.SetMaxBuf(maxHTMLFormattingTagBytes)
		kind := z.Next()
		scanned += len(z.Raw())
		if scanned > maxHTMLBudgetScanBytes || z.Err() == nethtml.ErrBufferExceeded {
			return ErrHTMLResourceLimit
		}
		if kind == nethtml.ErrorToken {
			if z.Err() != io.EOF {
				return z.Err()
			}
			continue
		}
		if kind != nethtml.StartTagToken && kind != nethtml.SelfClosingTagToken {
			continue
		}
		weight := int64(128)
		_, more := z.TagName()
		for more {
			_, _, more = z.TagAttr()
			weight += 64
			totalWeight += 64
		}
		if weight > largestWeight {
			largestWeight = weight
		}
		if exceedsBudget() {
			return ErrHTMLResourceLimit
		}
	}
	return nil
}

// value starts immediately after '<' or '</'. Only ASCII HTML tag delimiters
// count; inspecting at most seven bytes avoids rescanning long malformed names.
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
