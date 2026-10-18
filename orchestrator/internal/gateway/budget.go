package gateway

import "context"

// Budget is checked before every forwarded request. A non-nil error rejects
// the request with 429. Phase 3 implements the SPEC.md §7.5 limits.
type Budget interface {
	Check(ctx context.Context, c Caller) error
}

// NoBudget allows everything.
type NoBudget struct{}

// Check always allows.
func (NoBudget) Check(context.Context, Caller) error { return nil }
