package render

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Options controls entry HTML rewriting after sanitizing.
type Options struct {
	Full        bool
	ReadMoreURL string
	LocalUsers  map[string]string
	JournalURL  func(name string) string
	SignImage   func(src string) string
}

// RenderBody sanitizes and rewrites entry HTML.
func RenderBody(raw string, opt Options) string {
	clean := Sanitize(raw)
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(clean), ctx)
	if err != nil {
		return stripScheme(Sanitize(raw))
	}
	root := &html.Node{Type: html.ElementNode, Data: "div"}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	transform(root, opt, false)
	var buf bytes.Buffer
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&buf, c)
	}
	return stripScheme(buf.String())
}

func stripScheme(s string) string {
	const needle = "javascript:"
	lower := strings.ToLower(s)
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lower[i:], needle)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		b.WriteString(s[i : i+j])
		i += j + len(needle)
	}
}

func transform(n *html.Node, opt Options, inCut bool) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode {
			stripDanger(c)
			switch strings.ToLower(c.Data) {
			case "lj":
				n.InsertBefore(renderUser(c, opt), c)
				n.RemoveChild(c)
			case "lj-cut":
				if opt.Full {
					transform(c, opt, false)
					unwrap(n, c)
				} else {
					transform(c, opt, true)
					n.InsertBefore(renderCut(c), c)
					n.RemoveChild(c)
				}
			case "iframe":
				if !AllowedFrame(attr(c, "src")) {
					n.RemoveChild(c)
				} else {
					transform(c, opt, inCut)
				}
			case "img":
				rewriteImg(c, opt)
				if inCut {
					setAttr(c, "loading", "lazy")
				}
				transform(c, opt, inCut)
			default:
				transform(c, opt, inCut)
			}
		}
		c = next
	}
}

func unwrap(parent, cut *html.Node) {
	for cut.FirstChild != nil {
		ch := cut.FirstChild
		cut.RemoveChild(ch)
		parent.InsertBefore(ch, cut)
	}
	parent.RemoveChild(cut)
}

func renderUser(n *html.Node, opt Options) *html.Node {
	name := firstAttr(n, "user", "comm", "site")
	name = strings.ToLower(strings.TrimSpace(name))
	span := &html.Node{Type: html.ElementNode, Data: "span"}
	span.Attr = []html.Attribute{{Key: "class", Val: "ljuser"}}
	if !validUser(name) {
		span.AppendChild(&html.Node{Type: html.TextNode, Data: name})
		return span
	}
	img := &html.Node{Type: html.ElementNode, Data: "img"}
	img.Attr = []html.Attribute{
		{Key: "src", Val: "/static/userhead.svg"},
		{Key: "class", Val: "userhead"},
		{Key: "alt", Val: ""},
		{Key: "width", Val: "16"},
		{Key: "height", Val: "16"},
	}
	href := ""
	if opt.JournalURL != nil {
		href = opt.JournalURL(name)
	}
	if opt.LocalUsers != nil {
		if local, ok := opt.LocalUsers[name]; ok && local != "" {
			href = local
		}
	}
	a := &html.Node{Type: html.ElementNode, Data: "a"}
	a.Attr = []html.Attribute{{Key: "href", Val: href}}
	a.AppendChild(&html.Node{Type: html.TextNode, Data: name})
	span.AppendChild(img)
	span.AppendChild(&html.Node{Type: html.TextNode, Data: " "})
	span.AppendChild(a)
	return span
}

func renderCut(n *html.Node) *html.Node {
	text := strings.TrimSpace(attr(n, "text"))
	if text == "" {
		text = "Read more"
	}
	text = trimRunes(text, 80)
	details := &html.Node{Type: html.ElementNode, Data: "details", DataAtom: atom.Details}
	details.Attr = []html.Attribute{{Key: "class", Val: "cut"}}
	summary := &html.Node{Type: html.ElementNode, Data: "summary", DataAtom: atom.Summary}
	summary.AppendChild(&html.Node{Type: html.TextNode, Data: text})
	details.AppendChild(summary)
	for n.FirstChild != nil {
		ch := n.FirstChild
		n.RemoveChild(ch)
		details.AppendChild(ch)
	}
	return details
}

func trimRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for k := 0; k < n; k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

func rewriteImg(n *html.Node, opt Options) {
	src := attr(n, "src")
	if src == "" || strings.HasPrefix(src, "/static/") || strings.HasPrefix(src, "/userpics/") || strings.HasPrefix(src, "/img?") {
		return
	}
	if opt.SignImage == nil {
		setAttr(n, "src", "")
		return
	}
	signed := opt.SignImage(src)
	if signed == "" {
		removeAttr(n, "src")
		return
	}
	setAttr(n, "src", signed)
}

func stripDanger(n *html.Node) {
	var attrs []html.Attribute
	for _, a := range n.Attr {
		k := strings.ToLower(a.Key)
		if strings.HasPrefix(k, "on") {
			continue
		}
		if k == "style" && badStyle(a.Val) {
			continue
		}
		attrs = append(attrs, a)
	}
	n.Attr = attrs
}

func badStyle(s string) bool {
	l := strings.ToLower(s)
	for _, bad := range []string{"position", "z-index", "expression", "url(", "behavior", "@import", "fixed"} {
		if strings.Contains(l, bad) {
			return true
		}
	}
	return false
}

func validUser(s string) bool {
	if len(s) < 1 || len(s) > 25 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func firstAttr(n *html.Node, keys ...string) string {
	for _, k := range keys {
		if v := attr(n, k); v != "" {
			return v
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func removeAttr(n *html.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			out = append(out, a)
		}
	}
	n.Attr = out
}

// HasText reports whether HTML contains visible text.
func HasText(s string) bool {
	nodes, err := html.ParseFragment(strings.NewReader(s), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return strings.TrimSpace(s) != ""
	}
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "noscript":
				return false
			}
		}
		if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" {
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	for _, n := range nodes {
		if walk(n) {
			return true
		}
	}
	return false
}
