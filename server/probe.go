package server

import (
	"context"
	"errors"
)

// Probe asks a /health endpoint whether the app is alive. It returns nil only
// if the endpoint answers 200 with {"status":"ok"}.
//
// Why this exists: the production Docker image is "distroless" (no shell, no
// curl), so Docker's HEALTHCHECK can't run `curl /health`. Instead the app
// binary checks itself: `/app healthcheck` calls Probe and exits 0 or 1.
func Probe(ctx context.Context, url string) error {
	return errors.New("not implemented")
}
