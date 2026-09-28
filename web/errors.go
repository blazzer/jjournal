package web

import (
	"errors"
	"net/http"
	"strconv"

	"journal/app"
)

// WriteError maps an app error onto an HTTP status. Other errors are 500.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var inv app.Invalid
	var nf app.NotFound
	var fb app.Forbidden
	var cf app.Conflict
	var rl app.RateLimited
	switch {
	case errors.As(err, &nf):
		http.NotFound(w, r)
	case errors.As(err, &inv):
		http.Error(w, inv.Msg, http.StatusBadRequest)
	case errors.As(err, &fb):
		http.Error(w, fb.Msg, http.StatusForbidden)
	case errors.As(err, &cf):
		http.Error(w, cf.Msg, http.StatusConflict)
	case errors.As(err, &rl):
		sec := int(rl.RetryAfter.Seconds())
		if sec < 1 {
			sec = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(sec))
		http.Error(w, rl.Error(), http.StatusTooManyRequests)
	default:
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}
}
