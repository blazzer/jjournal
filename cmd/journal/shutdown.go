package main

import "context"

// step is one shutdown action. Order is the order they run.
type step struct {
	name string
	fn   func(context.Context) error
}

// shutdown runs steps in order. The caller lists them as SPEC §4.5:
// stop accepting HTTP and drain, stop the scheduler, stop the backup job, close the store.
func shutdown(ctx context.Context, steps []step) error {
	var first error
	for _, s := range steps {
		if err := s.fn(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}
