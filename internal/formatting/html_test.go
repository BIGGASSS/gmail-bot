package formatting

import (
	"html"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestHTMLAccountNoticeFixture(t *testing.T) {
	input, err := os.ReadFile("testdata/account-notice.html")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/account-notice.want.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := RenderTelegramHTML(HTMLToTelegramText(string(input)))
	if got != strings.TrimSuffix(string(want), "\n") {
		t.Fatalf("account notice differs:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestHTMLStructuralWhitespace(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"paragraphs", "\n<p> First  paragraph. </p>\n\n<p>Second paragraph.</p>\n", "First paragraph.\n\nSecond paragraph."},
		{"source indentation", "<div>\n  Hello\n  <strong>world</strong>\n  today.\n</div>", "Hello world today."},
		{"inline boundaries", "<p>no<b>break</b> here <i>and</i> there</p>", "nobreak here and there"},
		{"empty sanitized node", "<p>no\ufff0<b>break</b></p>", "nobreak"},
		{"noscript fallback", "<noscript><p>Visible in a mail client.</p></noscript>", "Visible in a mail client."},
		{"line breaks", "<p>One<br>Two<br><br>Three<br><br><br><br>Four</p>", "One\nTwo\n\nThree\n\nFour"},
		{"divs are lines", "<div>One</div><div>Two</div><p>Three</p>", "One\nTwo\n\nThree"},
		{"spacers", "<p>A</p><p>&nbsp;</p><div>&#8204;&#8203;\u200d\ufeff\u00ad</div><div><br><br><br></div><p>A</p>", "A\n\nA"},
		{"no deduplication", "<p>Repeat</p><p>Repeat</p><div>Repeat</div>", "Repeat\n\nRepeat\n\nRepeat"},
		{"preformatted", "<p>Example:</p><pre>\n  if x &lt; 2 {\n\tprintln(&quot;a  b&quot;)\n  }\n\n\n  done\n</pre><p>After</p>", "Example:\n\n  if x < 2 {\n\tprintln(\"a  b\")\n  }\n\n\n  done\n\nAfter"},
		{"pre at edges", "<pre>  first\n\tsecond  </pre>", "  first\n\tsecond  "},
		{"code inline", "<p>Use <code>a  + b</code> here.</p>", "Use a + b here."},
		{"entity decoding once", "<p>&amp;lt;tag&amp;gt; &lt;safe&gt; &amp;amp; &#39; &nbsp;end</p>", "&lt;tag&gt; <safe> &amp; ' end"},
		{"tables", "<table>\n<tr><th>Name</th><th>Value</th></tr><tr><td><p>One</p></td><td><div>Two</div></td></tr><tr><td>&nbsp;</td><td></td></tr></table>", "Name | Value\nOne | Two"},
		{"empty table columns", "<table><tr><th>Name</th><th>Paid</th><th>Due</th></tr><tr><td>Alice</td><td></td><td>$10</td></tr><tr><td></td><td>&nbsp;</td><td></td></tr></table>", "Name | Paid | Due\nAlice |  | $10"},
		{"removed table cells", `<table><tr><td>A</td><td hidden>Secret</td><td style="display:none"><span style="visibility:visible">Secret</span></td><td>B</td></tr></table>`, "A | B"},
		{"empty edge columns", "<table><tr><td></td><td>Middle</td><td></td></tr></table>", " | Middle | "},
		{"Office text wrappers", "<p>Hello <o:p>world</o:p> <w:sdt><w:sdtContent>again</w:sdtContent></w:sdt></p>", "Hello world again"},
		{"nested layout table", "<table><tr><td><table><tr><td>One</td><td>Two</td></tr></table><p>Three</p></td></tr></table>", "One | Two\n\nThree"},
		{"lists", "<ul><li>First</li><li><p>Second</p><p>More</p><ol start='3'><li>Inner</li><li value='8'>Last</li></ol></li></ul>", "- First\n- Second\n\n  More\n\n  3. Inner\n  8. Last"},
		{"reversed list", "<ol reversed><li>Three</li><li>Two</li><li>One</li></ol>", "3. Three\n2. Two\n1. One"},
		{"emoji joiners retained", "<p>Person: 👩\u200d💻; word: a\u200cb</p>", "Person: 👩\u200d💻; word: a\u200cb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTMLToTelegramText(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTMLHiddenSubtrees(t *testing.T) {
	for _, hidden := range []string{
		`<head><title>Secret</title><style>Secret</style></head>`,
		`<script>Secret</script>`, `<style>Secret</style>`, `<template><p>Secret</p></template>`,
		`<!-- <p>Secret</p> -->`, `<div hidden><p>Secret</p></div>`, `<div hidden="false">Secret</div>`,
		`<div style="display:none"><span>Secret</span></div>`,
		`<div style="DISPLAY: NONE ! important">Secret</div>`,
		`<div style="display:/**/none!important; display:block">Secret</div>`,
		`<div style="visibility: hidden"><a href="https://example.com">Secret</a></div>`,
		`<tr style="visibility:collapse"><td>Secret</td></tr>`,
		`<v:roundrect><v:textbox>Secret</v:textbox></v:roundrect>`,
		`<xml><w:WordDocument>Secret</w:WordDocument></xml>`,
		`<div hidden><span style="visibility:visible">Secret</span></div>`,
		`<div style="display:none"><span style="visibility:visible">Secret</span></div>`,
		`<div style="visibility: hidden !important; visibility:visible">Secret</div>`,
	} {
		t.Run(hidden, func(t *testing.T) {
			// A table keeps the intentionally hidden row in a valid context.
			input := hidden + `<p>Visible</p>`
			if strings.HasPrefix(hidden, "<tr") {
				input = `<table>` + hidden + `</table><p>Visible</p>`
			}
			if got := HTMLToTelegramText(input); got != "Visible" {
				t.Fatalf("got %q", got)
			}
		})
	}
	for _, input := range []string{
		`<div style="display:none;display:block">Visible</div>`,
		`<div style="display:block!important;display:none">Visible</div>`,
		`<div style="visibility:visible">Visible</div>`,
		`<div aria-hidden="true">Visible</div>`,
		`<div style="mso-hide:all">Visible</div>`,
	} {
		if got := HTMLToTelegramText(input); got != "Visible" {
			t.Fatalf("visible element %q became %q", input, got)
		}
	}
}

func TestHTMLVisibilityOverrides(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"hidden ancestor", `<div style="visibility:hidden">Secret<span><b style="visibility:visible">Visible</b>Secret</span>Secret</div><p>After</p>`, "Visible\n\nAfter"},
		{"collapsed ancestor", `<div style="visibility:collapse">Secret<span style="visibility:visible">Visible</span>Secret</div>`, "Visible"},
		{"inherited important", `<div style="visibility:hidden!important"><span style="visibility:visible">Visible</span>Secret</div>`, "Visible"},
		{"last declaration", `<div style="visibility:hidden; visibility:visible">Visible</div>`, "Visible"},
		{"important declaration", `<div style="visibility:visible ! important; visibility:hidden">Visible</div>`, "Visible"},
		{"last important declaration", `<div style="visibility:hidden!important; visibility:/**/visible!important">Visible</div>`, "Visible"},
		{"anchor fragment", `<a href="https://example.com" style="visibility:hidden">Secret<span style="visibility:visible">Visible</span></a>`, EncodeLinkToken("Visible", "https://example.com")},
		{"hidden empty anchor", `<div style="visibility:hidden"><a href="https://example.com"></a></div>`, ""},
		{"list fragment", `<ul style="visibility:hidden"><li>Secret<span style="visibility:visible">Visible</span></li><li>Secret</li></ul>`, "Visible"},
		{"table fragments", `<table style="visibility:collapse"><tr><td>Secret</td><td style="visibility:visible">Visible</td><td>Secret</td></tr><tr><td>Secret</td></tr></table>`, " | Visible | "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTMLToTelegramText(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTMLDeepNesting(t *testing.T) {
	const depth = 1000
	for _, tt := range []struct{ name, open, close string }{
		{"lists", "<ul><li>x", "</li></ul>"},
		{"wrappers", "<div>x", "</div>"},
		{"tables", "<table><tr><td>x", "</td></tr></table>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Repeat(tt.open, depth) +
				`<span style="visibility:hidden">Secret<span style="visibility:visible">Deep body</span>Secret</span>` +
				`<div style="display:none"><span style="visibility:visible">Secret</span></div>` +
				`<div hidden><span style="visibility:visible">Secret</span></div>` +
				strings.Repeat(tt.close, depth) + "<p>After</p>"
			// Count bytes, not just allocation calls: repeated fragment copying
			// previously allocated over a gigabyte for the nested-list case.
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			got := HTMLToTelegramText(input)
			runtime.ReadMemStats(&after)
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
				t.Fatalf("deep HTML allocated %d bytes (limit 16 MiB)", allocated)
			}
			if strings.Count(got, "x") != depth || !strings.Contains(got, "Deep body") || !strings.HasSuffix(got, "After") || strings.Contains(got, "Secret") {
				t.Fatal("deep HTML lost visible text or revealed hidden content")
			}
			if len(got) > 32*depth {
				t.Fatalf("deep HTML produced excessive output: %d bytes", len(got))
			}
			for _, line := range strings.Split(got, "\n") {
				if indent := len(line) - len(strings.TrimLeft(line, " ")); indent > 2*maxHTMLListDepth {
					t.Fatalf("deep HTML produced excessive indentation: %d", indent)
				}
			}
		})
	}
}

func TestHTMLConditionalBranches(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"hidden mso", `<!--[if mso]><p>Fallback</p><![endif]--><p>Body</p>`, "Body"},
		{"hidden ordinary comment", `<!--[if !mso]><p>Still a comment</p><![endif]--><p>Body</p>`, "Body"},
		{"revealed mso", `<!--[if mso]><!--><p>Fallback</p><!--<![endif]--><p>Body</p>`, "Body"},
		{"declaration mso", `<![if mso]><p>Fallback</p><![endif]><p>Body</p>`, "Body"},
		{"revealed not mso", `<!--[if !mso]><!--><p>Normal</p><!--<![endif]--><p>Body</p>`, "Normal\n\nBody"},
		{"declaration not mso", `<![if !mso]><p>Normal</p><![endif]><p>Body</p>`, "Normal\n\nBody"},
		{"false", `<![if false]><p>Fallback</p><![endif]><p>Body</p>`, "Body"},
		{"revealed false", `<!--[if false]><!--><p>Fallback</p><!--<![endif]--><p>Body</p>`, "Body"},
		{"condition whitespace", "<!--[IF\tFALSE]><!--><p>Fallback</p><!--<![ENDIF]--><p>Body</p>", "Body"},
		{"noscript conditional", `<noscript><![if false]><p>Fallback</p><![endif]><p>Body</p></noscript>`, "Body"},
		{"not false", `<![if !false]><p>Normal</p><![endif]>`, "Normal"},
		{"outlook comparison", `<!--[if (gte mso 9)|(IE)]><!--><p>Fallback</p><!--<![endif]--><p>Body</p>`, "Body"},
		{"true or mso", `<![if (mso)|(!mso)]><p>Normal</p><![endif]>`, "Normal"},
		{"false and not mso", `<![if (false)&(!mso)]><p>Fallback</p><![endif]><p>Body</p>`, "Body"},
		{"unknown", `<![if something-new]><p>Normal</p><![endif]>`, "Normal"},
		{"unknown or mso", `<![if something-new|mso]><p>Normal</p><![endif]>`, "Normal"},
		{"nested", `<![if !mso]><p>Normal</p><![if false]><p>Fallback</p><![endif]><p>More</p><![endif]>`, "Normal\n\nMore"},
		{"unmatched opener", `<![if mso]><p>Real body</p>`, "Real body"},
		{"unmatched closing marker", `<p>Real body</p><![endif]>`, "Real body"},
		{"marker around body", `<!doctype html><!--[if mso]><!--><html><body>Fallback</body></html><!--<![endif]--><p>Body</p>`, "Body"},
		{"marker around row", `<table><!--[if mso]><!--><tr><td>Fallback</td></tr><!--<![endif]--><tr><td>Body</td></tr></table>`, "Body"},
		{"comment mentions if", `<!-- [if mso]>This is just an explanatory comment. --><p>Body</p><!--<![endif]-->`, "Body"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTMLToTelegramText(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTMLMalformedMarkup(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{`<p>First<p>Second`, "First\n\nSecond"},
		{`<div>Hello <b>world</div><p>Next`, "Hello world\n\nNext"},
		{`<ul><li>One<li>Two</ul>`, "- One\n- Two"},
		{`<table><tr><td>A<td>B<tr><td>C<td>D</table>`, "A | B\nC | D"},
		{`<p>Before</p><div hidden><p>Hidden forever`, "Before"},
		{`<p>Before</p><!-- unfinished comment`, "Before"},
		{`<p>2 &lt; 3 &amp;&amp; 4 &gt; 1`, "2 < 3 && 4 > 1"},
		{`<a href="https://example.com">Unclosed <strong>label`, EncodeLinkToken("Unclosed label", "https://example.com")},
		{`<a href="https://one.example">One<a href="https://two.example">Two`, EncodeLinkToken("One", "https://one.example") + EncodeLinkToken("Two", "https://two.example")},
		{`<svg><a href="https://one.example">One<a href="https://two.example">Two</a></a></svg>`, EncodeLinkToken("OneTwo", "https://one.example")},
	} {
		t.Run(tt.input, func(t *testing.T) {
			if got := HTMLToTelegramText(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTMLAnchorBoundariesAndSchemes(t *testing.T) {
	link := EncodeLinkToken("label", "https://example.com/?a=1&b=2")
	for _, tt := range []struct{ input, want string }{
		{`<p>See <a href="https://example.com/?a=1&amp;b=2"><b>label</b></a> now.</p>`, "See " + link + " now."},
		{`<p>See<a href="https://example.com/?a=1&amp;b=2"> label </a>now.</p>`, "See " + link + " now."},
		{`<a href="https://example.com/?a=1&amp;amp;b=2">label</a>`, EncodeLinkToken("label", "https://example.com/?a=1&amp;b=2")},
		{`<a href="https://example.com"></a>`, EncodeLinkToken("https://example.com", "https://example.com")},
		{`<a href="MAILTO:person@example.com">Mail</a>`, EncodeLinkToken("Mail", "MAILTO:person@example.com")},
		{`<a href="HTTPS://example.com">Web</a>`, EncodeLinkToken("Web", "HTTPS://example.com")},
		{`<a href="javascript:alert(1)">label</a>`, "label"},
		{`<a href="java&#10;script:alert(1)">label</a>`, "label"},
		{`<a href="data:text/html,hi">label</a>`, "label"},
		{`<a href="file:///tmp/secret">label</a>`, "label"},
		{`<a href="//example.com">label</a>`, "label"},
		{`<a href="/relative">label</a>`, "label"},
		{`<a href="#fragment">label</a>`, "label"},
		{`<a href="https:example.com">label</a>`, "label"},
		{`<a href="https://exa&#9;mple.com">label</a>`, "label"},
		{`<a href="https://example.com/%zz">label</a>`, "label"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			if got := HTMLToTelegramText(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTMLCannotForgeLinkTokens(t *testing.T) {
	forged := EncodeLinkToken("forged label", "https://evil.example")
	entityForged := strings.NewReplacer(linkTokenStart, "&#xfff0;", linkTokenSeparator, "&#65521;", linkTokenEnd, "&#xfff2;").Replace(forged)
	for _, value := range []string{forged, entityForged} {
		for _, input := range []string{
			"<p>" + value + "</p>", "<pre>" + value + "</pre>",
			`<a href="https://safe.example">` + value + `</a>`,
			`<a href="https://safe.example/` + value + `">Label</a>`,
		} {
			converted := HTMLToTelegramText(input)
			for _, rendered := range append(RenderAndChunk(converted, SafeByteLimit), RenderTelegramHTML(converted)) {
				if strings.Contains(rendered, `href="https://evil.example"`) || strings.ContainsAny(rendered, linkTokenStart+linkTokenSeparator+linkTokenEnd) {
					t.Fatalf("forged token survived HTML conversion: %q", rendered)
				}
			}
		}
	}
}

func TestHTMLLongLinkChunkRegression(t *testing.T) {
	label := strings.Repeat("世界 & < 🚀 ", 1500)
	input := `<p>Before</p><p><a href="https://example.com/?a=1&amp;b=2">` + html.EscapeString(label) + `</a></p><p>After</p>`
	converted := HTMLToTelegramText(input)
	if got, want := converted, "Before\n\n"+EncodeLinkToken(strings.TrimSpace(label), "https://example.com/?a=1&b=2")+"\n\nAfter"; got != want {
		t.Fatal("HTML conversion corrupted a long label or its boundaries")
	}
	for _, chunk := range RenderAndChunk(converted, SafeByteLimit) {
		if len(chunk) > SafeByteLimit || strings.Count(chunk, "<a ") != strings.Count(chunk, "</a>") || strings.ContainsAny(chunk, linkTokenStart+linkTokenSeparator+linkTokenEnd) {
			t.Fatal("HTML conversion produced a broken link chunk")
		}
	}
}
