package web

import (
	"context"
	"strings"
	"time"

	"journal/render"
	"journal/store"
)

type storePauses struct{ s *store.Store }

func (p storePauses) List(ctx context.Context) (map[string]time.Time, error) {
	return p.s.ListHostPauses(ctx)
}

func (p storePauses) Put(ctx context.Context, host string, until time.Time, reason string) error {
	return p.s.PutHostPause(ctx, host, until, reason)
}

// FetchImages downloads queued image URLs on the image lane.
func FetchImages(ctx context.Context, jobs <-chan string, pics, images *render.Proxy) {
	for {
		select {
		case <-ctx.Done():
			return
		case raw := <-jobs:
			if strings.Contains(raw, "userpic") {
				_, _ = pics.Download(ctx, raw)
			} else {
				_, _ = images.Download(ctx, raw)
			}
		}
	}
}

// CacheJanitor evicts cache files until the total is under maxBytes.
func CacheJanitor(ctx context.Context, dirs []string, maxBytes int64) {
	if maxBytes <= 0 {
		maxBytes = 256 << 20
	}
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = render.Evict(dirs, maxBytes)
		}
	}
}
