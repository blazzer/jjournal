package web

import (
	"strconv"
	"strings"

	"journal/lj"
)

// ParseUserPath matches /~username, /~username/friends, /~username/profile, and /~username/id.html.
func ParseUserPath(path string) (user, kind string, entryID int64, ok bool) {
	if !strings.HasPrefix(path, "/~") {
		return "", "", 0, false
	}
	rest := strings.TrimPrefix(path, "/~")
	rest = strings.Trim(rest, "/")
	if rest == "" || strings.Contains(rest, "..") {
		return "", "", 0, false
	}
	name, tail, hasTail := strings.Cut(rest, "/")
	user, err := lj.NormalizeUsername(name)
	if err != nil || strings.Contains(tail, "/") {
		return "", "", 0, false
	}
	if !hasTail || tail == "" {
		return user, "journal", 0, true
	}
	switch tail {
	case "friends":
		return user, "friends", 0, true
	case "profile":
		return user, "profile", 0, true
	}
	if !strings.HasSuffix(tail, ".html") {
		return "", "", 0, false
	}
	id, err := strconv.ParseInt(strings.TrimSuffix(tail, ".html"), 10, 64)
	if err != nil || id <= 0 {
		return "", "", 0, false
	}
	return user, "entry", id, true
}
