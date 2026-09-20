package formatting

import (
	"fmt"
	"strings"
	"testing"
)

func TestHTMLFlattenPreservesLinks(t *testing.T) {
	for _, wrapper := range []struct{ name, open, close string }{
		{"tables", strings.Repeat("<table><tr><td>", 16), strings.Repeat("</td></tr></table>", 16)},
		{"wrappers", strings.Repeat("<div>", maxHTMLRenderDepth), strings.Repeat("</div>", maxHTMLRenderDepth)},
		{"lists", strings.Repeat("<ul><li>", maxHTMLListDepth+1), strings.Repeat("</li></ul>", maxHTMLListDepth+1)},
	} {
		for _, tt := range []struct{ name, input, label, href string }{
			{"safe", `<a href="https://example.com/invoice?a=1&amp;b=2">Open <b>invoice</b></a>`, "Open invoice", "https://example.com/invoice?a=1&b=2"},
			{"mail", `<a href="mailto:person@example.com">Mail</a>`, "Mail", "mailto:person@example.com"},
			{"empty", `<a href="https://example.com"></a>`, "https://example.com", "https://example.com"},
			{"unsafe", `<a href="javascript:alert(1)">Label</a>`, "Label", ""},
			{"hidden", `<div hidden><a href="https://example.com">Secret</a></div>`, "", ""},
			{"invisible empty", `<a style="visibility:hidden" href="https://example.com"></a>`, "", ""},
			{"visibility override", `<a style="visibility:hidden" href="https://example.com">Secret<b style="visibility:visible">Label</b></a>`, "Label", "https://example.com"},
			{"foreign nested", `<svg><a href="https://example.com">Outer<a href="https://other.example">Inner</a></a></svg>`, "OuterInner", "https://example.com"},
		} {
			t.Run(wrapper.name+"/"+tt.name, func(t *testing.T) {
				input := wrapper.open + tt.input + wrapper.close
				for _, links := range []bool{false, true} {
					got, err := extractHTMLText(input, links)
					if err != nil {
						t.Fatal(err)
					}
					want := tt.label
					if tt.href != "" {
						if links {
							want = EncodeLinkToken(want, tt.href)
						} else if want != tt.href {
							want += " <" + tt.href + ">"
						}
					}
					if wrapper.name == "lists" && want != "" {
						want = strings.Repeat("- ", maxHTMLListDepth) + want
					}
					if got != want {
						t.Fatalf("links=%v: got %q, want %q", links, got, want)
					}
				}
			})
		}
	}
}

func TestHTMLFlattenAnchorBoundaries(t *testing.T) {
	open, close := strings.Repeat("<div>", maxHTMLRenderDepth), strings.Repeat("</div>", maxHTMLRenderDepth)
	for _, tt := range []struct{ input, want string }{
		{`See<a href="https://example.com"> label </a>now`, "See " + EncodeLinkToken("label", "https://example.com") + " now"},
		{`<a href="https://example.com"><p>Label</p></a>After`, EncodeLinkToken("Label", "https://example.com") + "\n\nAfter"},
		{"<pre>  <a href=\"https://example.com\"> Label\n </a> end  </pre>", "  " + EncodeLinkToken(" Label\n ", "https://example.com") + " end  "},
	} {
		if got := mustHTMLText(t, open+tt.input+close); got != tt.want {
			t.Fatalf("got %q, want %q", got, tt.want)
		}
	}
}

func TestHTMLWriterTrailingNewlines(t *testing.T) {
	for _, suffix := range []int{0, 1, 2, 3, 60000} {
		for _, breaks := range []int{1, 2} {
			var w htmlTextWriter
			w.append("Start" + strings.Repeat("\n", suffix))
			w.boundary(breaks)
			w.append("End")
			count := suffix
			if breaks > count {
				count = breaks
			}
			if w.out.String() != "Start"+strings.Repeat("\n", count)+"End" {
				t.Fatalf("suffix=%d breaks=%d: changed existing newlines", suffix, breaks)
			}
		}
	}
	input := strings.Repeat("<pre>\n\n</pre>", 1000) + "End"
	if got := mustHTMLText(t, input); got != strings.Repeat("\n", 1001)+"End" {
		t.Fatal("repeated preformatted newlines were not preserved")
	}
}

func BenchmarkHTMLWriterTrailingNewlines(b *testing.B) {
	// Exercise the writer directly, independently of the pre-DOM markup limit.
	// Each iteration is the writer path taken by a <pre>\n\n</pre> element.
	for _, count := range []int{30000, 60000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var w htmlTextWriter
				for j := 0; j < count; j++ {
					w.boundary(2)
					w.append("\n")
					w.boundary(2)
				}
				w.append("End")
				if w.out.Len() != count+1+len("End") {
					b.Fatal("incorrect newline count")
				}
			}
		})
	}
}
