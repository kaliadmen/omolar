package router

// Step 7: dispatch, freezing, 404, options, and wiring to normalizePath.
// Test levels (design §11.0): most tests call ServeHTTP directly (L2); the
// last ones go through a real server (L3) and raw TCP (L4).
//
// Not here on purpose: 405 and the Allow header (step 8, Quinze's task 2) and
// trailing-slash redirects (step 9, Quinze's task 3). Requests that match a
// path but not its method aren't tested yet.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// echo is a handler that reports which route ran and what it saw, e.g.
// "user GET /users/{id} id=42".
func echo(name string, keys ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := name + " " + r.Pattern
		for _, k := range keys {
			out += " " + k + "=" + r.PathValue(k)
		}
		io.WriteString(w, out)
	})
}

func mustHandle(t *testing.T, rt *Router, pattern string, h http.Handler) {
	t.Helper()
	if err := rt.Handle(pattern, h); err != nil {
		t.Fatalf("Handle(%q): %v", pattern, err)
	}
}

// do serves one request. target may be any escaped path, including ones
// httptest.NewRequest would refuse or rewrite: the URL is set by hand.
func do(rt http.Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/", nil)
	p, q, _ := strings.Cut(target, "?")
	req.URL.RawPath = p
	req.URL.Path = p // EscapedPath uses RawPath only if it's a valid encoding of Path...
	if decoded, err := pathUnescapeForTest(p); err == nil {
		req.URL.Path = decoded // ...so give it the real decoded Path, like net/http would
	}
	req.URL.RawQuery = q
	req.RequestURI = target
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	return rec
}

func pathUnescapeForTest(p string) (string, error) {
	r, err := http.NewRequest("GET", "http://x"+p, nil)
	if err != nil {
		return "", err
	}
	return r.URL.Path, nil
}

// countingHandler records whether it ran, to prove refused requests never
// reach a handler.
type countingHandler struct{ n atomic.Int32 }

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { c.n.Add(1) }

// ---- Dispatch ------------------------------------------------------------------

func TestServeHTTP_Dispatch(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "GET /{$}", echo("home"))
	mustHandle(t, rt, "GET /users", echo("users"))
	mustHandle(t, rt, "GET /users/me", echo("me"))
	mustHandle(t, rt, "GET /users/{id}", echo("user", "id"))
	mustHandle(t, rt, "GET /orgs/{org}/repos/{repo}", echo("repo", "org", "repo"))
	mustHandle(t, rt, "GET /static/{p...}", echo("static", "p"))
	mustHandle(t, rt, "/any", echo("any"))
	mustHandle(t, rt, "POST /users", echo("create"))

	cases := []struct{ method, target, want string }{
		{"GET", "/", "home GET /{$}"},
		{"GET", "/users", "users GET /users"},
		{"GET", "/users/me", "me GET /users/me"},
		{"GET", "/users/42", "user GET /users/{id} id=42"},
		{"GET", "/users/caf%C3%A9", "user GET /users/{id} id=café"}, // values are decoded
		{"GET", "/orgs/acme/repos/omolar", "repo GET /orgs/{org}/repos/{repo} org=acme repo=omolar"},
		{"GET", "/static/", "static GET /static/{p...} p="},
		{"GET", "/static/app.js", "static GET /static/{p...} p=app.js"},
		{"GET", "/static/css/site.css", "static GET /static/{p...} p=css/site.css"},
		{"GET", "/static/css/", "static GET /static/{p...} p=css/"},
		{"GET", "/static/caf%C3%A9/a%20b", "static GET /static/{p...} p=café/a b"},
		{"HEAD", "/users/42", "user GET /users/{id} id=42"}, // HEAD is served by GET
		{"DELETE", "/any", "any /any"},
		{"POST", "/users", "create POST /users"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rec := do(rt, tc.method, tc.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestServeHTTP_UnknownPathValueIsEmpty(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "GET /users/{id}", echo("user", "id", "nope"))
	if got := do(rt, "GET", "/users/7").Body.String(); got != "user GET /users/{id} id=7 nope=" {
		t.Errorf("got %q", got)
	}
}

// ---- 404 -----------------------------------------------------------------------

func TestServeHTTP_NotFound(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "GET /users/{id}", echo("user"))
	for _, target := range []string{"/", "/nope", "/users", "/users/", "/users/1/extra"} {
		rec := do(rt, "GET", target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
		if rec.Body.String() != "404 page not found\n" {
			t.Errorf("GET %s body = %q, want the standard 404 text", target, rec.Body.String())
		}
	}
}

func TestServeHTTP_NotFoundOnEmptyRouter(t *testing.T) {
	if rec := do(New(), "GET", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("empty router: %d, want 404", rec.Code)
	}
}

func TestWithNotFound(t *testing.T) {
	custom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "custom 404 for "+r.URL.Path)
	})
	rt := New(WithNotFound(custom))
	rec := do(rt, "GET", "/missing")
	if rec.Code != 404 || rec.Body.String() != "custom 404 for /missing" {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}

	// nil means "use the default", never "crash on a nil handler".
	if rec := do(New(WithNotFound(nil)), "GET", "/x"); rec.Code != 404 {
		t.Errorf("WithNotFound(nil): %d, want the default 404", rec.Code)
	}
}

// ---- 400 / 414 from normalization ----------------------------------------------

func TestServeHTTP_BadPathsAre400(t *testing.T) {
	h := &countingHandler{}
	rt := New()
	mustHandle(t, rt, "/{p...}", h) // would match anything, if allowed through

	for _, target := range []string{
		"/static/a%2F..%2F..%2Fetc%2Fpasswd", "/a%2Fb", "/%2e%2e", "/a/.%2e",
		"/a%00b", "/a%0d%0aX:y", "/%FF", "/%C0%AF", "/a%5Cb", `/a\b`, "*",
		// (A malformed escape like /%zz never reaches a handler: net/http's
		// own parser rejects it. TestRawRequests checks that end to end.)
	} {
		rec := do(rt, "GET", target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", target, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("GET %s Content-Type = %q, want text/plain", target, ct)
		}
		// Don't reflect the client's input back (it could be crafted to look
		// like something else in a browser or a log viewer).
		if body := rec.Body.String(); strings.Contains(body, "%") || strings.Contains(body, "evil") {
			t.Errorf("GET %s: body %q echoes the request path", target, body)
		}
	}
	if n := h.n.Load(); n != 0 {
		t.Errorf("the handler ran %d times for refused paths, want 0", n)
	}
}

func TestServeHTTP_TooLongIs414(t *testing.T) {
	h := &countingHandler{}
	rt := New()
	mustHandle(t, rt, "/{p...}", h)

	long := "/" + strings.Repeat("a", defaultMaxPathLength)
	deep := strings.Repeat("/a", defaultMaxSegments+1)
	for name, target := range map[string]string{"long": long, "deep": deep} {
		if rec := do(rt, "GET", target); rec.Code != http.StatusRequestURITooLong {
			t.Errorf("%s path: %d, want 414", name, rec.Code)
		}
	}
	if h.n.Load() != 0 {
		t.Error("the handler ran for a 414")
	}
}

func TestLimitOptions(t *testing.T) {
	rt := New(WithMaxPathLength(20), WithMaxSegments(3))
	mustHandle(t, rt, "/{p...}", echo("x"))

	cases := []struct {
		target string
		want   int
	}{
		{"/a/b/c", 200},
		{"/a/b/c/d", 414},                    // 4 segments > 3
		{"/a/b/", 200},                       // 3 segments counting the trailing slash
		{"/a/b/c/", 414},                     // 4 with the trailing slash (approved in review)
		{"/" + strings.Repeat("a", 19), 200}, // 20 bytes
		{"/" + strings.Repeat("a", 20), 414}, // 21 bytes
	}
	for _, tc := range cases {
		if rec := do(rt, "GET", tc.target); rec.Code != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.target, rec.Code, tc.want)
		}
	}

	// n < 1 means the defaults.
	def := New(WithMaxPathLength(0), WithMaxSegments(-5))
	mustHandle(t, def, "/{p...}", echo("x"))
	if rec := do(def, "GET", strings.Repeat("/a", defaultMaxSegments)); rec.Code != 200 {
		t.Errorf("default segment limit not applied: %d", rec.Code)
	}
	if rec := do(def, "GET", strings.Repeat("/a", defaultMaxSegments+1)); rec.Code != 414 {
		t.Errorf("default segment limit not applied: %d", rec.Code)
	}
}

// ---- Canonicalization redirects (design §6.6) -----------------------------------

func TestServeHTTP_CanonicalRedirect(t *testing.T) {
	h := &countingHandler{}
	rt := New()
	mustHandle(t, rt, "/{p...}", h)

	cases := []struct{ target, location string }{
		{"/a/../b", "/b"},
		{"/a/../b?x=1&y=%20", "/b?x=1&y=%20"}, // the query is kept as sent
		{"/a//b/", "/a/b/"},
		{"/./a", "/a"},
		{"//evil.com", "/evil.com"}, // never a protocol-relative redirect
		{"/%41/../b", "/b"},
	}
	for _, tc := range cases {
		for _, method := range []string{"GET", "HEAD"} {
			rec := do(rt, method, tc.target)
			if rec.Code != http.StatusTemporaryRedirect {
				t.Errorf("%s %s = %d, want 307", method, tc.target, rec.Code)
				continue
			}
			if loc := rec.Header().Get("Location"); loc != tc.location {
				t.Errorf("%s %s Location = %q, want %q", method, tc.target, loc, tc.location)
			}
		}
	}
	if h.n.Load() != 0 {
		t.Error("the handler ran for a redirected request")
	}
}

func TestServeHTTP_NonCanonicalBodyMethodsAre400(t *testing.T) {
	// Q3: a POST/PUT/... to a non-canonical path is refused, not bounced.
	h := &countingHandler{}
	rt := New()
	mustHandle(t, rt, "/{p...}", h)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if rec := do(rt, method, "/a/../b"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s /a/../b = %d, want 400", method, rec.Code)
		}
	}
	if h.n.Load() != 0 {
		t.Error("the handler ran")
	}
}

func TestServeHTTP_ValidationBeforeRedirect(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "/{p...}", echo("x"))
	for _, target := range []string{"/a/../%2F", "//%5Cevil.com", "/./%00"} {
		if rec := do(rt, "GET", target); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d (Location %q), want 400", target, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// ---- Handle --------------------------------------------------------------------

func TestHandle_Errors(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "GET /u/{id}", echo("u"))

	var pe *PatternError
	if err := rt.Handle("get /x", echo("x")); !errors.As(err, &pe) {
		t.Errorf("bad pattern: %v, want *PatternError", err)
	}
	var ce *ConflictError
	if err := rt.Handle("GET /u/{name}", echo("x")); !errors.As(err, &ce) {
		t.Errorf("conflicting pattern: %v, want *ConflictError", err)
	} else if ce.Existing != "GET /u/{id}" {
		t.Errorf("ConflictError.Existing = %q", ce.Existing)
	}
	if err := rt.Handle("GET /x", nil); err == nil {
		t.Error("a nil handler should be an error")
	}
	if err := rt.HandleFunc("GET /y", nil); err == nil {
		t.Error("a nil handler func should be an error")
	}

	// None of the failures registered anything.
	want := []Route{{"GET", "GET /u/{id}"}}
	if got := rt.Routes(); !slices.Equal(got, want) {
		t.Errorf("Routes() = %v, want %v", got, want)
	}
}

func TestHandleFunc(t *testing.T) {
	rt := New()
	if err := rt.HandleFunc("GET /f/{id}", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "f "+r.PathValue("id"))
	}); err != nil {
		t.Fatal(err)
	}
	if got := do(rt, "GET", "/f/9").Body.String(); got != "f 9" {
		t.Errorf("got %q", got)
	}
}

func TestRoutes(t *testing.T) {
	rt := New()
	for _, p := range []string{"POST /users", "GET /users/{id}", "/health", "GET /users"} {
		mustHandle(t, rt, p, echo("x"))
	}
	want := []Route{{"", "/health"}, {"GET", "GET /users"}, {"POST", "POST /users"}, {"GET", "GET /users/{id}"}}
	if got := rt.Routes(); !slices.Equal(got, want) {
		t.Errorf("Routes() = %v, want %v", got, want)
	}
	rt.Freeze()
	if got := rt.Routes(); !slices.Equal(got, want) {
		t.Errorf("after Freeze, Routes() = %v, want %v", got, want)
	}
	got := rt.Routes()
	got[0].Pattern = "changed"
	if rt.Routes()[0].Pattern != "/health" {
		t.Error("Routes() returned the router's own slice")
	}
}

func TestMatch(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "GET /users/{id}/files/{p...}", echo("x"))
	mustHandle(t, rt, "GET /users/{id}", echo("x"))

	m, ok := rt.Match("GET", "/users/7/files/a/b")
	want := Match{Pattern: "GET /users/{id}/files/{p...}", Params: []Param{{"id", "7"}, {"p", "a/b"}}}
	if !ok || m.Pattern != want.Pattern || !slices.Equal(m.Params, want.Params) {
		t.Errorf("Match = %+v %v, want %+v", m, ok, want)
	}
	if m, ok := rt.Match("HEAD", "/users/caf%C3%A9"); !ok || m.Params[0].Value != "café" {
		t.Errorf("HEAD /users/café: %+v %v", m, ok)
	}
	for _, p := range []string{"/nope", "/users/../users/7", "/users/a%2Fb", ""} {
		if _, ok := rt.Match("GET", p); ok {
			t.Errorf("Match(%q) = true, want false", p)
		}
	}
	if rt.Frozen() {
		t.Error("Match must not freeze the router")
	}
}

// ---- Freezing (design §7, test plan F1-F6) ---------------------------------------

func TestFreeze(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "GET /a", echo("a"))
	if rt.Frozen() {
		t.Fatal("a new router should not be frozen")
	}
	rt.Freeze()
	rt.Freeze() // F1: idempotent
	if !rt.Frozen() {
		t.Fatal("Frozen() = false after Freeze")
	}

	// F2: Handle after Freeze is refused and changes nothing.
	if err := rt.Handle("GET /b", echo("b")); !errors.Is(err, ErrFrozen) {
		t.Errorf("Handle after Freeze: %v, want ErrFrozen", err)
	}
	if got := rt.Routes(); len(got) != 1 {
		t.Errorf("Routes() after a refused Handle = %v", got)
	}
	if rec := do(rt, "GET", "/b"); rec.Code != 404 {
		t.Errorf("GET /b = %d, want 404: the refused route must not exist", rec.Code)
	}
	if rec := do(rt, "GET", "/a"); rec.Code != 200 {
		t.Errorf("GET /a = %d, want 200", rec.Code)
	}
}

func TestFirstRequestFreezes(t *testing.T) {
	// F3: a standalone router that's never frozen freezes on its first request.
	rt := New()
	mustHandle(t, rt, "GET /a", echo("a"))
	do(rt, "GET", "/a")
	if !rt.Frozen() {
		t.Fatal("the first ServeHTTP should freeze the router")
	}
	if err := rt.Handle("GET /b", echo("b")); !errors.Is(err, ErrFrozen) {
		t.Errorf("Handle after the first request: %v, want ErrFrozen", err)
	}
}

func TestFreezeRacingHandle(t *testing.T) {
	// F4: every Handle either succeeds (and its route works) or gets
	// ErrFrozen; never a half-added route. Run with -race.
	for run := range 20 {
		rt := New()
		results := make([]error, 50)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range results {
			wg.Go(func() {
				<-start
				results[i] = rt.Handle(fmt.Sprintf("GET /r%d", i), echo(fmt.Sprint(i)))
			})
		}
		wg.Go(func() { <-start; rt.Freeze() })
		close(start)
		wg.Wait()

		for i, err := range results {
			target := fmt.Sprintf("/r%d", i)
			code := do(rt, "GET", target).Code
			switch {
			case err == nil && code != 200:
				t.Fatalf("run %d: Handle(%s) succeeded but the route answers %d", run, target, code)
			case errors.Is(err, ErrFrozen) && code != 404:
				t.Fatalf("run %d: Handle(%s) was refused but the route answers %d", run, target, code)
			case err != nil && !errors.Is(err, ErrFrozen):
				t.Fatalf("run %d: unexpected error %v", run, err)
			}
		}
	}
}

func TestConcurrentRequests(t *testing.T) {
	// F5: many goroutines against a frozen router; every answer correct.
	rt := New()
	mustHandle(t, rt, "GET /users/{id}", echo("user", "id"))
	mustHandle(t, rt, "GET /static/{p...}", echo("static", "p"))
	mustHandle(t, rt, "GET /health", echo("health"))
	rt.Freeze()

	var wg sync.WaitGroup
	for g := range 50 {
		wg.Go(func() {
			for i := range 200 {
				var target, want string
				switch i % 4 {
				case 0:
					target, want = fmt.Sprintf("/users/%d-%d", g, i), fmt.Sprintf("user GET /users/{id} id=%d-%d", g, i)
				case 1:
					target, want = fmt.Sprintf("/static/%d/%d.js", g, i), fmt.Sprintf("static GET /static/{p...} p=%d/%d.js", g, i)
				case 2:
					target, want = "/health", "health GET /health"
				case 3:
					target, want = "/nope", "404 page not found\n"
				}
				if got := do(rt, "GET", target).Body.String(); got != want {
					t.Errorf("GET %s = %q, want %q", target, got, want)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestConcurrentHandle(t *testing.T) {
	// F6: Handle from several goroutines before freezing: all routes present.
	rt := New()
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			if err := rt.Handle(fmt.Sprintf("GET /c/%d", i), echo("c")); err != nil {
				t.Errorf("Handle: %v", err)
			}
		})
	}
	wg.Wait()
	if n := len(rt.Routes()); n != 100 {
		t.Errorf("%d routes registered, want 100", n)
	}
}

// ---- L3: a real server -----------------------------------------------------------

func TestRealServer(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "GET /users/{id}", echo("user", "id"))
	mustHandle(t, rt, "GET /b", echo("b"))
	srv := httptest.NewServer(rt)
	defer srv.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
		// Don't follow redirects: we want to see the 307 itself.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	get := func(method, path string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	if resp, body := get("GET", "/users/42"); resp.StatusCode != 200 || body != "user GET /users/{id} id=42" {
		t.Errorf("GET /users/42 = %d %q", resp.StatusCode, body)
	}
	if resp, _ := get("GET", "/nope"); resp.StatusCode != 404 {
		t.Errorf("GET /nope = %d", resp.StatusCode)
	}
	if resp, _ := get("GET", "/a%2Fb"); resp.StatusCode != 400 {
		t.Errorf("GET /a%%2Fb = %d, want 400 (the client sends %%2F as is)", resp.StatusCode)
	}
	if resp, _ := get("GET", "/x/../b?q=1"); resp.StatusCode != 307 || resp.Header.Get("Location") != "/b?q=1" {
		t.Errorf("GET /x/../b = %d Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := get("HEAD", "/users/42"); resp.StatusCode != 200 || body != "" {
		t.Errorf("HEAD /users/42 = %d with body %q; a real server sends no body", resp.StatusCode, body)
	}
}

// ---- L4: raw TCP, for request lines a normal client won't send -------------------

func TestRawRequests(t *testing.T) {
	rt := New()
	mustHandle(t, rt, "/{p...}", echo("any", "p"))
	srv := httptest.NewServer(rt)
	defer srv.Close()

	cases := []struct {
		requestTarget string
		status        int
		location      string
	}{
		{"/a/../b", 307, "/b"},
		{"//evil.com", 307, "/evil.com"},
		{"/./a/./b", 307, "/a/b"},
		{`/\evil.com`, 400, ""},
		{"/a%2Fb", 400, ""},
		{"/%2e%2e/etc/passwd", 400, ""},
		{"/%zz", 400, ""}, // refused by net/http's parser before the router runs
		{"/fine/path", 200, ""},
	}
	for _, tc := range cases {
		t.Run(tc.requestTarget, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n", tc.requestTarget)

			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("reading response: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.location != "" && resp.Header.Get("Location") != tc.location {
				t.Errorf("Location = %q, want %q", resp.Header.Get("Location"), tc.location)
			}
		})
	}
}
