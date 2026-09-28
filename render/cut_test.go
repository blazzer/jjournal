package render

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestCuts(t *testing.T) {
	sign := func(src string) string {
		if strings.HasPrefix(src, "https://cdn.example/") {
			return "/img?u=signed"
		}
		return ""
	}
	cases := []struct {
		name string
		raw  string
		full bool
		want []string
		bad  []string
	}{
		{
			name: "nested",
			raw:  `<lj-cut text="outer"><p>a</p><lj-cut text="inner"><p>b</p></lj-cut></lj-cut>`,
			want: []string{`<details class="cut"><summary>outer</summary>`, `<details class="cut"><summary>inner</summary><p>b</p></details>`},
		},
		{
			name: "user iframe image",
			raw:  `<lj-cut text="more"><lj user="alice"></lj><iframe src="https://www.youtube.com/embed/abc123"></iframe><img src="https://cdn.example/pic.png" alt="pic"></lj-cut>`,
			want: []string{`class="ljuser"`, `youtube.com/embed/abc123`, `/img?u=signed`, `loading="lazy"`},
		},
		{
			name: "hostile children",
			raw:  `<lj-cut text="x"><script>alert(1)</script><a href="javascript:alert(1)">z</a><img src="https://cdn.example/pic.png" onerror="alert(1)"></lj-cut>`,
			want: []string{`<details class="cut">`, `/img?u=signed`},
			bad:  []string{"<script", "javascript:", "onerror"},
		},
		{
			name: "full unwraps",
			raw:  `<lj-cut text="more"><p>shown</p></lj-cut>`,
			full: true,
			want: []string{"<p>shown</p>"},
			bad:  []string{"<details", "more"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderBody(tc.raw, Options{Full: tc.full, SignImage: sign, LocalUsers: map[string]string{"alice": "/~alice/profile"}})
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Fatalf("missing %q in %s", w, out)
				}
			}
			low := strings.ToLower(out)
			for _, b := range tc.bad {
				if strings.Contains(low, strings.ToLower(b)) {
					t.Fatalf("contains %q in %s", b, out)
				}
			}
		})
	}
}

func TestCutTextRuneBoundary(t *testing.T) {
	text := strings.Repeat("я", 81)
	out := RenderBody(`<lj-cut text="`+text+`">body</lj-cut>`, Options{})
	summary := summaryText(t, out)
	if utf8.RuneCountInString(summary) != 80 {
		t.Fatalf("runes %d %q", utf8.RuneCountInString(summary), summary)
	}
	if !utf8.ValidString(summary) {
		t.Fatal("invalid utf-8")
	}
	if !strings.Contains(out, "body") {
		t.Fatal(out)
	}
}

func TestImageOutsideCutNotLazy(t *testing.T) {
	out := RenderBody(`<img src="https://cdn.example/pic.png" alt="pic">`, Options{
		SignImage: func(string) string { return "/img?u=signed" },
	})
	if strings.Contains(out, "loading=") {
		t.Fatal(out)
	}
}

func summaryText(t *testing.T, doc string) string {
	t.Helper()
	nodes, err := html.ParseFragment(strings.NewReader(doc), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "summary" {
			if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				text = n.FirstChild.Data
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	if text == "" {
		t.Fatalf("no summary in %s", doc)
	}
	return text
}

func FuzzRenderBody(f *testing.F) {
	f.Add("<p>hi</p>")
	f.Add(`<lj-cut text="ещё"><script>alert(1)</script><img src="javascript:x" onerror="alert(1)"><a href="javascript:alert(1)">x</a></lj-cut>`)
	f.Add(`<lj user="alice"><iframe src="https://evil.example/"></iframe>`)
	f.Fuzz(func(t *testing.T, raw string) {
		out := RenderBody(raw, Options{SignImage: func(string) string { return "" }})
		assertSafeHTML(t, out)
		out = RenderBody(raw, Options{Full: true})
		assertSafeHTML(t, out)
	})
}

func assertSafeHTML(t *testing.T, out string) {
	t.Helper()
	low := strings.ToLower(out)
	if strings.Contains(low, "<script") || strings.Contains(low, "javascript:") {
		t.Fatalf("%s", out)
	}
	nodes, err := html.ParseFragment(strings.NewReader(out), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		t.Fatal(err)
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if strings.EqualFold(n.Data, "script") {
				t.Fatalf("script node in %s", out)
			}
			for _, a := range n.Attr {
				k := strings.ToLower(a.Key)
				if strings.HasPrefix(k, "on") {
					t.Fatalf("attr %s in %s", a.Key, out)
				}
				if (k == "href" || k == "src") && strings.Contains(strings.ToLower(a.Val), "javascript:") {
					t.Fatalf("%s=%s", k, a.Val)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
}
