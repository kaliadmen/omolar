package router

import (
	"errors"

	"github.com/kaliadmen/omolar/internal/routetree"
)

// PatternError reports a malformed route pattern, with the byte offset of the
// problem.
//
// GO CONCEPT: a type alias (`=`). router.PatternError IS routetree.PatternError,
// not a copy: the same type under a second name. It lets the public router
// package expose an error type defined in an internal package, so callers can
// use errors.As with it without importing internal/routetree (which they
// aren't allowed to).
type PatternError = routetree.PatternError

// ConflictError reports a route that could match the same request as an
// existing one, where neither is more specific. Example is a request that
// matches both.
type ConflictError = routetree.ConflictError

// ErrFrozen is returned by Handle after Freeze.
var ErrFrozen = errors.New("router: routes cannot be added after Freeze")
