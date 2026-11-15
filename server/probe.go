package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Probe asks a /health endpoint whether the app is alive. It returns nil only
// if the endpoint answers 200 with {"status":"ok"}.
//
// Why this exists: the production Docker image is "distroless" (no shell, no
// curl), so Docker's HEALTHCHECK can't run `curl /health`. Instead the app
// binary checks itself: `/app healthcheck` calls Probe and exits 0 or 1.
func Probe(ctx context.Context, url string) error {
	// NewRequestWithContext ties the request to ctx: when ctx's deadline
	// passes, the request is abandoned, even mid-connection.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe: %s returned %d", url, resp.StatusCode)
	}

	// Security habit: never read an unbounded body from the network. A
	// misbehaving server could send gigabytes. 4KB is plenty for health JSON.
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); err != nil {
		return fmt.Errorf("probe: response is not health JSON (want {\"status\":\"ok\"}): %w", err)
	}
	if body.Status != "ok" {
		return fmt.Errorf("probe: status is %q, want \"ok\"", body.Status)
	}
	return nil
}
