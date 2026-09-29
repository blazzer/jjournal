// Package modern is the enhanced reading front-end.
package modern

import (
	"embed"

	"journal/web/classic"
)

//go:embed templates/*.html
var assets embed.FS

// Front renders the modern interface with the shared handlers.
type Front struct {
	*classic.Front
}

// New parses the modern templates.
func New() (*Front, error) {
	inner, err := classic.Parse(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Front{Front: inner}, nil
}

// UI identifies this front-end.
func (f *Front) UI() string { return "modern" }
