package render

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

var (
	colorRe = regexp.MustCompile(`^(?:#[0-9a-fA-F]{3,8}|[a-zA-Z]{1,20}|rgba?\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}(?:\s*,\s*(?:0|1|0?\.\d+))?\s*\))$`)
	alignRe = regexp.MustCompile(`^(?:left|right|center|justify)$`)
	weightRe = regexp.MustCompile(`^(?:normal|bold|bolder|lighter|[1-9]00)$`)
	styleRe  = regexp.MustCompile(`^(?:normal|italic|oblique)$`)
	decoRe   = regexp.MustCompile(`^(?:none|underline|overline|line-through)$`)
	httpRe   = regexp.MustCompile(`^https?://[^\s"'<>]+$`)
	hrefRe   = regexp.MustCompile(`^(?:https?://[^\s"'<>]+|mailto:[^\s"'<>]+)$`)
	numRe    = regexp.MustCompile(`^\d{1,4}$`)
	frameRe  = regexp.MustCompile(`^https://(?:www\.youtube\.com/embed/|www\.youtube-nocookie\.com/embed/|player\.vimeo\.com/video/)[A-Za-z0-9_-]+(?:[?&][A-Za-z0-9_.=%-]*)*$`)
)

// Policy is the entry HTML allowlist.
func Policy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(
		"a", "b", "blockquote", "br", "code", "div", "em", "h1", "h2", "h3", "h4", "h5", "h6",
		"hr", "i", "img", "li", "ol", "p", "pre", "q", "s", "small", "span", "strike", "strong",
		"sub", "sup", "table", "tbody", "td", "tfoot", "th", "thead", "tr", "u", "ul", "abbr",
		"cite", "del", "ins", "center", "font", "big", "lj", "lj-cut", "iframe",
	)
	p.SkipElementsContent("script", "style", "noscript", "object", "embed", "form", "textarea", "select", "button", "input", "meta", "link", "base")
	p.AllowAttrs("href").Matching(hrefRe).OnElements("a")
	p.AllowAttrs("title").OnElements("a", "abbr", "img")
	p.RequireNoFollowOnLinks(true)
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)
	p.AllowAttrs("src").Matching(httpRe).OnElements("img")
	p.AllowAttrs("alt").OnElements("img")
	p.AllowAttrs("width", "height").Matching(numRe).OnElements("img", "iframe")
	p.AllowAttrs("src").Matching(frameRe).OnElements("iframe")
	p.AllowAttrs("title", "allow", "allowfullscreen", "frameborder").OnElements("iframe")
	p.AllowAttrs("user", "site", "comm").Matching(regexp.MustCompile(`^[A-Za-z0-9_-]{1,25}$`)).OnElements("lj")
	p.AllowAttrs("text").OnElements("lj-cut")
	p.AllowNoAttrs().OnElements("lj", "lj-cut")
	p.AllowAttrs("color", "face").OnElements("font")
	p.AllowAttrs("size").Matching(numRe).OnElements("font")
	p.AllowAttrs("align").Matching(alignRe).OnElements("p", "div", "td", "th", "h1", "h2", "h3", "h4", "h5", "h6")
	blocks := []string{"span", "p", "div", "font", "li", "td", "th", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote"}
	p.AllowStyles("color").Matching(colorRe).OnElements(blocks...)
	p.AllowStyles("background-color").Matching(colorRe).OnElements(blocks...)
	p.AllowStyles("font-weight").Matching(weightRe).OnElements(blocks...)
	p.AllowStyles("font-style").Matching(styleRe).OnElements(blocks...)
	p.AllowStyles("text-decoration").Matching(decoRe).OnElements(blocks...)
	p.AllowStyles("text-align").Matching(alignRe).OnElements(blocks...)
	return p
}

var policy = Policy()

// Sanitize strips hostile HTML and keeps a LiveJournal-like tag set.
func Sanitize(s string) string {
	return policy.Sanitize(s)
}

// AllowedFrame reports whether an iframe src is YouTube or Vimeo.
func AllowedFrame(src string) bool {
	return frameRe.MatchString(src)
}
