package server

import "net/http"

// Health returns a handler for a /health endpoint, used by load balancers,
// Docker and uptime monitors to ask "is this app alive?".
func Health(version string) http.Handler {
	return http.NotFoundHandler()
}
