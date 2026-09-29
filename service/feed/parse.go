// Package feed reads public RSS and Atom documents. It does not fetch them.
package feed

import (
	"bytes"
	"encoding/xml"
	"html"
	"strings"
	"time"
)

// Item is one public post.
type Item struct {
	ID     string
	Title  string
	HTML   string
	Author string
	URL    string
	When   time.Time
}

// Parse reads an RSS or Atom document. A document with no items is an error.
func Parse(data []byte) ([]Item, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var items []Item
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "item", "entry":
			var raw entryXML
			if err := dec.DecodeElement(&raw, &start); err != nil {
				return nil, err
			}
			if item, ok := raw.item(); ok {
				items = append(items, item)
			}
		}
	}
	if len(items) == 0 {
		return nil, errEmpty
	}
	return items, nil
}

var errEmpty = errString("feed: empty")

type errString string

func (e errString) Error() string { return string(e) }

type entryXML struct {
	Title       string      `xml:"title"`
	ID          string      `xml:"id"`
	GUID        string      `xml:"guid"`
	Links       []linkXML   `xml:"link"`
	Content     string      `xml:"content"`
	Summary     string      `xml:"summary"`
	Description string      `xml:"description"`
	Updated     string      `xml:"updated"`
	Published   string      `xml:"published"`
	PubDate     string      `xml:"pubDate"`
	Authors     []authorXML `xml:"author"`
	Creator     string      `xml:"creator"`
}

type linkXML struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Body string `xml:",chardata"`
}

type authorXML struct {
	Name string `xml:"name"`
}

func (e entryXML) item() (Item, bool) {
	htmlBody := strings.TrimSpace(e.Content)
	if htmlBody == "" {
		htmlBody = strings.TrimSpace(e.Description)
	}
	if htmlBody == "" {
		htmlBody = strings.TrimSpace(e.Summary)
	}
	htmlBody = html.UnescapeString(htmlBody)
	id := strings.TrimSpace(e.ID)
	if id == "" {
		id = strings.TrimSpace(e.GUID)
	}
	author := strings.TrimSpace(e.Creator)
	if author == "" && len(e.Authors) > 0 {
		author = strings.TrimSpace(e.Authors[0].Name)
	}
	when := parseWhen(e.Updated, e.Published, e.PubDate)
	item := Item{
		ID:     id,
		Title:  html.UnescapeString(strings.TrimSpace(e.Title)),
		HTML:   htmlBody,
		Author: author,
		URL:    e.url(),
		When:   when,
	}
	if item.Title == "" && item.HTML == "" && item.ID == "" {
		return Item{}, false
	}
	if item.ID == "" {
		item.ID = item.URL
	}
	return item, item.ID != ""
}

func (e entryXML) url() string {
	var fallback string
	for _, l := range e.Links {
		href := strings.TrimSpace(l.Href)
		if href == "" {
			href = strings.TrimSpace(l.Body)
		}
		if href == "" {
			continue
		}
		if l.Rel == "" || l.Rel == "alternate" {
			return href
		}
		if fallback == "" {
			fallback = href
		}
	}
	return fallback
}

func parseWhen(values ...string) time.Time {
	layouts := []string{time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}
