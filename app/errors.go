package app

import "time"

// Invalid is a rejected input.
type Invalid struct {
	Field string
	Msg   string
}

func (e Invalid) Error() string { return e.Msg }

// NotFound is a missing object.
type NotFound struct{ Msg string }

func (e NotFound) Error() string {
	if e.Msg == "" {
		return "not found"
	}
	return e.Msg
}

// Forbidden is an authenticated caller who may not do this.
type Forbidden struct{ Msg string }

func (e Forbidden) Error() string {
	if e.Msg == "" {
		return "forbidden"
	}
	return e.Msg
}

// Conflict is a state clash.
type Conflict struct{ Msg string }

func (e Conflict) Error() string {
	if e.Msg == "" {
		return "conflict"
	}
	return e.Msg
}

// RateLimited asks the caller to wait.
type RateLimited struct{ RetryAfter time.Duration }

func (e RateLimited) Error() string { return "slow down" }
