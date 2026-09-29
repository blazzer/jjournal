package feed

import (
	"net/url"
	"strings"
)

// Canonical is the public feed URL for a service and journal.
func Canonical(service, journal string) string {
	journal = strings.TrimSpace(journal)
	switch service {
	case "livejournal":
		return "https://" + journal + ".livejournal.com/data/atom"
	case "dreamwidth":
		return "https://" + journal + ".dreamwidth.org/data/atom"
	case "rossia":
		return "https://lj.rossia.org/users/" + journal + "/data/rss"
	default:
		return journal
	}
}

// ParseURL reports which service a URL belongs to.
func ParseURL(raw string) (service, journal string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path := strings.Trim(u.Path, "/")
	switch {
	case host == "lj.rossia.org" || strings.HasSuffix(host, ".rossia.org"):
		name := strings.TrimPrefix(path, "users/")
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[:i]
		}
		if name == "" {
			return "", "", false
		}
		return "rossia", name, true
	case host == "dreamwidth.org" || strings.HasSuffix(host, ".dreamwidth.org"):
		name := strings.TrimSuffix(host, ".dreamwidth.org")
		if name == "" || name == "www" || name == host {
			name = firstPath(path)
		}
		if name == "" {
			return "", "", false
		}
		return "dreamwidth", name, true
	case host == "livejournal.com" || strings.HasSuffix(host, ".livejournal.com"):
		name := strings.TrimSuffix(host, ".livejournal.com")
		if name == "" || name == "www" || name == host {
			name = firstPath(path)
		}
		if name == "" {
			return "", "", false
		}
		return "livejournal", name, true
	default:
		if strings.Contains(path, "rss") || strings.Contains(path, "atom") || strings.HasSuffix(path, ".xml") {
			return "feed", raw, true
		}
	}
	return "", "", false
}

func firstPath(path string) string {
	if i := strings.IndexByte(path, '/'); i >= 0 {
		path = path[:i]
	}
	if path == "users" || path == "data" {
		return ""
	}
	return path
}
