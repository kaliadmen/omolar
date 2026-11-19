package router

// Step 8 integration: 405 Method Not Allowed with the Allow header from
// allowHeader (Quinze's Task 2). Design §6.4, test plan §11.3–11.5.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newRouter(t *testing.T, patterns ...string) *Router {
	t.Helper()
	rt := New()
	for _, p := range patterns {
		mustHandle(t, rt, p, echo(p, "id", "p", "name"))
	}
	return rt
}

type methodCase struct {
	id       string
	routes   []string
	method   string
	target   string
	status   int
	allow    string // the expected Allow header; "" = must be absent
	bodyFrom string // for 200: the pattern whose handler must have run
}

func runMethodCases(t *testing.T, cases []methodCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.id+" "+tc.method+" "+tc.target, func(t *testing.T) {
			rec := do(newRouter(t, tc.routes...), tc.method, tc.target)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.status, rec.Body.String())
			}
			allow, present := rec.Header()["Allow"]
			switch {
			case tc.allow == "" && present:
				t.Errorf("Allow = %q, want no Allow header", allow)
			case tc.allow != "" && rec.Header().Get("Allow") != tc.allow:
				t.Errorf("Allow = %q, want %q", rec.Header().Get("Allow"), tc.allow)
			}
			if tc.bodyFrom != "" && !strings.HasPrefix(rec.Body.String(), tc.bodyFrom+" ") {
				t.Errorf("body %q: the %q handler should have run", rec.Body.String(), tc.bodyFrom)
			}
		})
	}
}

// Design §11.3 (M rows) and §11.5 (H rows), through ServeHTTP.
func TestServeHTTP_MethodPrecedence(t *testing.T) {
	users := []string{"GET /users/{id}", "POST /users/new"}
	files := []string{"GET /files/{p...}", "PUT /files/{name}"}
	runMethodCases(t, []methodCase{
		// Precedence is unchanged: a method mismatch on one route falls back
		// to a less specific route that does accept the method.
		{"M6", users, "GET", "/users/new", 200, "", "GET /users/{id}"},
		{"M7", users, "POST", "/users/new", 200, "", "POST /users/new"},
		{"M8", users, "HEAD", "/users/new", 200, "", "GET /users/{id}"},
		{"M9", users, "DELETE", "/users/new", 405, "GET, HEAD, POST", ""},
		// Wildcards.
		{"M10", files, "GET", "/files/a", 200, "", "GET /files/{p...}"},
		{"M11", files, "PUT", "/files/a", 200, "", "PUT /files/{name}"},
		{"M12", files, "PUT", "/files/a/b", 405, "GET, HEAD", ""},
		{"M13", files, "DELETE", "/files/a", 405, "GET, HEAD, PUT", ""},
		{"rest slot, two methods", []string{"GET /static/{p...}", "PUT /static/{p...}"}, "DELETE", "/static/a/b/", 405, "GET, HEAD, PUT", ""},
		{"param", []string{"POST /orgs/{org}/repos"}, "GET", "/orgs/acme/repos", 405, "POST", ""},
		// OPTIONS is an ordinary method (Q5): no automatic answer.
		{"M14a", []string{"OPTIONS /x", "GET /x"}, "OPTIONS", "/x", 200, "", "OPTIONS /x"},
		{"M14b", []string{"OPTIONS /x", "GET /x"}, "POST", "/x", 405, "GET, HEAD, OPTIONS", ""},
		{"OPTIONS not synthesised", []string{"GET /x"}, "OPTIONS", "/x", 405, "GET, HEAD", ""},
		// Custom methods.
		{"M15a", []string{"PURGE /cache"}, "PURGE", "/cache", 200, "", "PURGE /cache"},
		{"M15b", []string{"PURGE /cache"}, "GET", "/cache", 405, "PURGE", ""},
		{"unknown request method", []string{"GET /x"}, "BREW", "/x", 405, "GET, HEAD", ""},
		{"M16 lower-case method", []string{"GET /x"}, "get", "/x", 405, "GET, HEAD", ""},
		// HEAD.
		{"H HEAD via GET", []string{"GET /x"}, "HEAD", "/x", 200, "", "GET /x"},
		{"H4 explicit HEAD wins", []string{"HEAD /x", "GET /x"}, "HEAD", "/x", 200, "", "HEAD /x"},
		{"H5 HEAD on POST-only", []string{"POST /x"}, "HEAD", "/x", 405, "POST", ""},
		{"H6 HEAD on rest", []string{"GET /static/{p...}"}, "HEAD", "/static/a/", 200, "", "GET /static/{p...}"},
		{"A3 HEAD-only route", []string{"HEAD /x"}, "GET", "/x", 405, "HEAD", ""},
		// A method-less route accepts every method, so its path never 405s (A8).
		{"A8", []string{"/x", "GET /x"}, "DELETE", "/x", 200, "", "/x"},
		// Duplicates from two routes on the same path are listed once.
		{"A7 dedup across leaves", []string{"GET /users/{id}", "GET /users/me"}, "DELETE", "/users/me", 405, "GET, HEAD", ""},
		{"union across leaves", []string{"GET /users/{id}", "PUT /users/{id}", "POST /users/me"}, "DELETE", "/users/me", 405, "GET, HEAD, POST, PUT", ""},
	})
}

// Design §11.4 N rows: 404 versus 405 versus 400.
func TestServeHTTP_404vs405vs400(t *testing.T) {
	runMethodCases(t, []methodCase{
		{"N1 unknown path", []string{"GET /x"}, "POST", "/y", 404, "", ""},
		{"N1 longer path", []string{"GET /x"}, "POST", "/x/y", 404, "", ""},
		{"N2 slash variant is not the same path", []string{"GET /tree/"}, "POST", "/tree", 404, "", ""},
		{"N2 other way round", []string{"GET /tree"}, "POST", "/tree/", 404, "", ""},
		// Validation runs first: an unsafe path leaks nothing about routes.
		{"N4 bad path", []string{"GET /a/{id}"}, "POST", "/a/x%2Fy", 400, "", ""},
		{"N4 non-canonical", []string{"GET /a/{id}"}, "POST", "/a/../a/x", 400, "", ""},
		{"N4 too long", []string{"GET /{p...}"}, "POST", "/" + strings.Repeat("a", defaultMaxPathLength), 414, "", ""},
	})
}

func TestServeHTTP_405Response(t *testing.T) {
	h := &countingHandler{}
	rt := New()
	mustHandle(t, rt, "GET /x", h)
	rec := do(rt, "DELETE", "/x")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if body := rec.Body.String(); body != "Method Not Allowed\n" {
		t.Errorf("body = %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if h.n.Load() != 0 {
		t.Error("the GET handler ran for a DELETE")
	}
}

func TestWithMethodNotAllowed(t *testing.T) {
	var sawAllow string
	custom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAllow = w.Header().Get("Allow") // N6: set before the handler runs
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		io.WriteString(w, `{"error":"method not allowed"}`)
	})
	rt := New(WithMethodNotAllowed(custom))
	mustHandle(t, rt, "GET /x", echo("x"))
	mustHandle(t, rt, "POST /x", echo("x"))

	rec := do(rt, "DELETE", "/x")
	if rec.Code != 405 || rec.Body.String() != `{"error":"method not allowed"}` {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
	if sawAllow != "GET, HEAD, POST" {
		t.Errorf("custom handler saw Allow = %q, want it already set", sawAllow)
	}
	if rec.Header().Get("Allow") != "GET, HEAD, POST" {
		t.Errorf("Allow = %q in the response", rec.Header().Get("Allow"))
	}

	// The custom 405 handler isn't used for 404s.
	if rec := do(rt, "DELETE", "/nope"); rec.Code != 404 || strings.Contains(rec.Body.String(), "error") {
		t.Errorf("a 404 went to the 405 handler: %d %q", rec.Code, rec.Body.String())
	}
	// nil means the default.
	def := New(WithMethodNotAllowed(nil))
	mustHandle(t, def, "GET /x", echo("x"))
	if rec := do(def, "DELETE", "/x"); rec.Code != 405 || rec.Body.String() != "Method Not Allowed\n" {
		t.Errorf("WithMethodNotAllowed(nil): %d %q", rec.Code, rec.Body.String())
	}
}

func TestNotFoundAndMethodNotAllowedAreSeparate(t *testing.T) {
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, "custom 404")
	})
	rt := New(WithNotFound(notFound))
	mustHandle(t, rt, "GET /x", echo("x"))
	if rec := do(rt, "DELETE", "/x"); rec.Code != 405 || rec.Body.String() == "custom 404" {
		t.Errorf("a 405 went to the 404 handler: %d %q", rec.Code, rec.Body.String())
	}
}

// L3: over a real connection (N7, and HEAD's empty body on a 405).
func TestRealServer_405(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "GET /users/{id}", echo("user"))
	mustHandle(t, rt, "PUT /users/{id}", echo("user"))
	srv := httptest.NewServer(rt)
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	for _, method := range []string{"DELETE", "PATCH"} {
		req, _ := http.NewRequest(method, srv.URL+"/users/7", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 405 || resp.Header.Get("Allow") != "GET, HEAD, PUT" || string(body) != "Method Not Allowed\n" {
			t.Errorf("%s: %d Allow=%q body=%q", method, resp.StatusCode, resp.Header.Get("Allow"), body)
		}
	}

	rtPost := New()
	mustHandle(t, rtPost, "POST /x", echo("x"))
	srv2 := httptest.NewServer(rtPost)
	defer srv2.Close()
	req, _ := http.NewRequest("HEAD", srv2.URL+"/x", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 405 || resp.Header.Get("Allow") != "POST" || len(body) != 0 {
		t.Errorf("HEAD on POST-only: %d Allow=%q body=%q", resp.StatusCode, resp.Header.Get("Allow"), body)
	}
}
