package server

import (
	"encoding/json"
	"net/http"
)

// Health returns a handler for a /health endpoint, used by load balancers,
// Docker and uptime monitors to ask "is this app alive?".
//
// Security note: it reports only "ok" and the version. Health endpoints are
// usually public, so they must not leak hostnames, config or dependency
// details an attacker could use.
func Health(version string) http.Handler {
	// Encode the body once, up front. It never changes, so there's no reason
	// to rebuild it on every request.
	body, _ := json.Marshal(map[string]string{"status": "ok", "version": version})

	// GO CONCEPT: http.HandlerFunc is a function type that has a ServeHTTP
	// method. Converting our function to it makes the function satisfy the
	// http.Handler interface.
	//
	// PATTERN: Adapter. http.HandlerFunc adapts "a plain function" to "the
	// http.Handler interface". Without it, every handler would need its own
	// struct type just to hang a ServeHTTP method on.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		// Headers must be set BEFORE WriteHeader/Write. Once the status line
		// goes out, header changes are silently ignored. (The old framework
		// had this bug in its maintenance-mode middleware.)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

		if r.Method == http.MethodHead {
			return // HEAD = same headers as GET, no body
		}
		w.Write(body)
	})
}
