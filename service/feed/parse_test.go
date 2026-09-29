package feed

import (
	"strings"
	"testing"
	"time"
)

func TestParseAtomAndRSS(t *testing.T) {
	atom := []byte(`<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>tag:example,1</id>
    <title>Hello</title>
    <updated>2024-05-01T00:00:00Z</updated>
    <author><name>ada</name></author>
    <link rel="alternate" href="https://example.com/1"/>
    <content type="html">&lt;p&gt;hi&lt;/p&gt;</content>
  </entry>
</feed>`)
	items, err := Parse(atom)
	if err != nil || len(items) != 1 {
		t.Fatal(err, len(items))
	}
	if items[0].ID != "tag:example,1" || items[0].Author != "ada" || items[0].HTML != "<p>hi</p>" || items[0].URL != "https://example.com/1" {
		t.Fatalf("%+v", items[0])
	}
	if !items[0].When.Equal(time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(items[0].When)
	}
	rss := []byte(`<rss><channel><item>
		<title>Bye</title>
		<link>https://example.com/2</link>
		<guid>2</guid>
		<pubDate>Wed, 01 May 2024 00:00:00 GMT</pubDate>
		<dc:creator>bob</dc:creator>
		<description>&lt;p&gt;there&lt;/p&gt;</description>
	</item></channel></rss>`)
	items, err = Parse(rss)
	if err != nil || len(items) != 1 || items[0].Author != "bob" || items[0].ID != "2" {
		t.Fatalf("%v %+v", err, items)
	}
	if _, err := Parse([]byte(`<rss></rss>`)); err == nil {
		t.Fatal("empty")
	}
}

func TestCanonicalAndParseURL(t *testing.T) {
	cases := []struct {
		service, journal, url string
	}{
		{"livejournal", "ada", "https://ada.livejournal.com/data/atom"},
		{"dreamwidth", "ada", "https://ada.dreamwidth.org/data/atom"},
		{"rossia", "ada", "https://lj.rossia.org/users/ada/data/rss"},
		{"feed", "https://example.com/rss", "https://example.com/rss"},
	}
	for _, tc := range cases {
		if got := Canonical(tc.service, tc.journal); got != tc.url {
			t.Fatalf("%s %s -> %s", tc.service, tc.journal, got)
		}
	}
	if _, _, ok := ParseURL("https://ada.livejournal.com/123.html"); !ok {
		t.Fatal("lj")
	}
	svc, journal, ok := ParseURL("https://lj.rossia.org/users/ada/data/rss")
	if !ok || svc != "rossia" || journal != "ada" {
		t.Fatalf("%s %s %v", svc, journal, ok)
	}
	if _, _, ok := ParseURL("https://example.com/index.html"); ok {
		t.Fatal("not a feed")
	}
	if svc, journal, ok := ParseURL("https://example.com/rss.xml"); !ok || svc != "feed" || !strings.Contains(journal, "example.com") {
		t.Fatalf("%s %s %v", svc, journal, ok)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`<rss><channel><item><title>a</title><guid>1</guid></item></channel></rss>`))
	f.Add([]byte(`<feed><entry><title>a</title><id>1</id></entry></feed>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		items, err := Parse(data)
		if err != nil {
			return
		}
		for _, item := range items {
			if item.ID == "" {
				t.Fatal("blank id")
			}
		}
	})
}
