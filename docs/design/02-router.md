# Design: Milestone 2 router

| | |
|---|---|
| **Status** | Proposed. Awaiting review before any implementation |
| **Branch** | `milestone-2/router` |
| **Replaces** | `http.ServeMux` inside `omolar.App` |
| **Go version** | 1.27 (all `ServeMux` behaviour below was checked against the go1.27.4 source and by running it) |

## Contents

1. [Decisions at a glance](#1-decisions-at-a-glance)
2. [Goals and non-goals](#2-goals-and-non-goals)
3. [What `http.ServeMux` does today](#3-what-httpservemux-does-today)
4. [Choosing the data structure](#4-choosing-the-data-structure)
5. [Architecture](#5-architecture)
6. [Routing rules](#6-routing-rules)
7. [Concurrency safety](#7-concurrency-safety)
8. [Memory and allocations](#8-memory-and-allocations)
9. [Algorithmic complexity](#9-algorithmic-complexity)
10. [Public API and `omolar.App` integration](#10-public-api-and-omolarapp-integration)
11. [Test plan](#11-test-plan)
12. [How this maps to C and C++](#12-how-this-maps-to-c-and-c)
13. [Work plan and who builds what](#13-work-plan-and-who-builds-what)
14. [Open questions for review](#14-open-questions-for-review)

---

## 1. Decisions at a glance

| Topic | Proposal |
|---|---|
| Data structure | **Segment trie**: one tree level per path segment (`/users/42` = 2 levels). Character-level radix compression is deferred until benchmarks justify it (§4) |
| Pattern syntax | The same as `ServeMux`, so existing code keeps working: `"GET /users/{id}"`, `{rest...}`, `{$}` |
| Precedence | The most specific route wins. In practice: literal beats `{param}`, which beats `{rest...}`, checked left to right |
| Ambiguity | Two routes that could match the same request, where neither is more specific, are **rejected when registered**, never resolved by luck at runtime |
| Methods | Part of the match. `HEAD` falls back to `GET`. A path that exists but not for this method gets **405** with an `Allow` header |
| Trailing slash | `/users/` means exactly `/users/`. A subtree needs an explicit `{rest...}` (**differs from ServeMux**, §6.5) |
| Unsafe paths | `%2F`, `%2e%2e`, `%00` and control characters in a segment get **400**. Over-long paths get **414** (§6.7) |
| Concurrency | **Build, then freeze.** Routes are added during setup; the first request freezes the router; lookups take no locks |
| Path values | Set with `r.SetPathValue`, so handlers use the standard `r.PathValue("id")`. `r.Pattern` is set too |
| Registration errors | Returned as values by `router.Router`. `omolar.App` collects them and refuses to start, listing all of them (§10.3) |
| Dependencies | None (standard library only) |

## 2. Goals and non-goals

**Goals**

- **Correct:** for a given request, exactly one route matches, or a well-defined 404/405/redirect/400 is returned. No route order surprises.
- **Secure by default:** the router never hands a handler a path value that means something different from what path cleaning checked (§3.3).
- **Fast enough, and measured:** zero allocations for static routes. Benchmarked against `ServeMux` on the same hardware.
- **Teachable:** the data structure lives in its own package, with no HTTP in it, so it can be read and tested like a textbook trie.
- **A drop-in for Milestone 1:** every existing `app.HandleFunc(...)` call and test keeps working.

**Non-goals for Milestone 2** (each could be its own feature later)

- Host-based routing (`example.com/path`). The `Host` header is client-controlled; this deserves its own design.
- Regex segment constraints (`{id:[0-9]+}`). Regex routers can be slowed down by crafted input (ReDoS). Typed constraints could come later.
- Route groups and per-route middleware. That's Milestone 3, which builds on this API.
- Changing routes while serving. Hot reload is out of scope (§7).
- `CONNECT` proxying and the `GODEBUG=httpmuxgo121` compatibility mode.

## 3. What `http.ServeMux` does today

### 3.1 How it works inside

From `src/net/http/routing_tree.go` and `pattern.go` (go1.27.4):

- **It's a decision tree.** The first level branches on host, the second on method, and every level after that on one path segment. Each node holds:
  - `children` for literal segments, in a hybrid "mapping": a slice for 8 or fewer entries, a map above that
  - one `emptyChild` for a `{param}`
  - one `multiChild` for a `{rest...}`
- **Matching** (`matchPath`) is recursive: try the literal child, then the single wildcard, then the multi wildcard. If one fails, back up and try the next. The source says plainly that this only gives "most specific wins" because conflicting patterns were rejected at registration.
- **Conflicts** (`comparePathsAndMethods`) put every pair of patterns into one of these classes: `equivalent`, `moreSpecific`, `moreGeneral`, `disjoint`, `overlaps`. Registering a pattern that is `equivalent` to, or `overlaps`, an existing one **panics**.
- **Locking:** every request takes a read lock (`mux.mu.RLock()` in `matchOrRedirect`), because `ServeMux` allows new routes to be registered while it's serving.
- **Path handling:** it matches against `r.URL.EscapedPath()`, unescaping one segment at a time, so `%2F` stays inside a segment. Paths are cleaned with `path.Clean`.

### 3.2 Behaviour, observed

Run against Go 1.27.4 with these routes registered: `GET /users/{id}`, `POST /users/new`, `GET /users/me`, `/files/`, `GET /static/{rest...}`, `GET /exact/{$}`, `GET`/`PUT`/`DELETE /items/{id}`, `GET /tree/`.

| Request | Result | What it shows |
|---|---|---|
| `GET /users/me` | 200 via `GET /users/me` | a literal beats `{id}` |
| `GET /users/new` | 200 via `GET /users/{id}`, `id="new"` | **the method is part of the match**: `POST /users/new` doesn't apply, so it falls back to `{id}` |
| `HEAD /users/42` | 200 via `GET /users/{id}` | HEAD is served by GET |
| `PATCH /items/1` | 405, `Allow: DELETE, GET, HEAD, PUT` | the methods in `Allow` are sorted, and HEAD is included |
| `get /users/42` (lower case) | 405 | methods are case-sensitive |
| `GET /files/a/b/c` | 200 via `/files/` | **a trailing slash means "the whole subtree"** |
| `GET /tree` | 307, `Location: /tree/` | redirects to add the slash, using 307 (not 301) |
| `POST /tree` | 405 | |
| `GET /users//42` | 307 to `/users/42` | path cleaning |
| `GET /static/css/../../etc/passwd` | 307 to `/etc/passwd` | path cleaning |
| path set to `//evil.com` | 307 to `/evil.com` | cleaning removes the `//` open-redirect trick |
| `GET /Users/42` | 404 | paths are case-sensitive |
| `GET /users/a%2Fb` | 200, `id="a/b"` | an encoded slash is decoded **into** the value |
| `GET /users/a%2e%2e` | 200, `id="a.."` | |
| `GET /static/a%2F..%2F..%2Fetc%2Fpasswd` | 200, **`rest="a/../../etc/passwd"`** | see §3.3 |

Registration checks:

| First pattern | Second pattern | Result |
|---|---|---|
| `GET /a/{x}` | `GET /{y}/b` | panic: they overlap (`/a/b` matches both, and neither is more specific) |
| `GET /u/{id}` | `GET /u/{name}` | panic: equivalent |
| `GET /a/b` | `GET /a/b` | panic: duplicate |
| `GET /a/{x}` | `POST /{y}/b` | allowed: the methods never overlap |

### 3.3 The security gap Omolar should close

`ServeMux` cleans the **encoded** path, where `%2F` isn't a separator, but it hands handlers the **decoded** value. So `/static/a%2F..%2F..%2Fetc%2Fpasswd` passes cleaning untouched, and the handler receives `rest = "a/../../etc/passwd"`. A handler that does the obvious thing:

```go
f, _ := os.Open(filepath.Join("public", r.PathValue("rest"))) // -> "etc/passwd": outside public/
```

has a path traversal bug. That bug is the handler's fault, but a security-minded router shouldn't make it easy. **Omolar's rule: a path value a handler receives must never contain a separator or a dot segment that the cleaning step didn't see.** Concretely (§6.7): a decoded `/`, `.` or `..` segment, `%00`, or a control character inside any segment gets 400.

(For serving files we'll also use `os.Root`, Go 1.24+, when static files arrive in Milestone 4. Two layers of defence.)

### 3.4 Keep, change or drop

| `ServeMux` behaviour | Omolar | Why |
|---|---|---|
| `"METHOD /path"` pattern strings, `{name}`, `{name...}`, `{$}` | **keep** | familiar, and every Milestone 1 route already uses them |
| `r.PathValue`, `r.Pattern` | **keep** | handlers stay portable between routers |
| Most specific wins, with conflicts rejected at registration | **keep** | predictable, and proven to work (§5.4) |
| HEAD served by GET; 405 with sorted `Allow` | **keep** | correct HTTP |
| Case-sensitive paths and methods | **keep** | case folding invites bypasses (`/Admin` vs `/admin`) |
| Path cleaning, with a redirect for GET/HEAD | **keep**, using 307 like ServeMux | |
| Redirecting non-GET requests (POST etc.) to a cleaned path | **change → 400** | a body-carrying request to a malformed path is a client bug or a probe. Don't bounce it (Q3) |
| A trailing slash means a subtree | **change → explicit `{rest...}`** | `"/admin/"` silently capturing `/admin/anything` is a classic accident (Q2) |
| Decoded `/` and dot segments reach handlers | **change → 400** | §3.3 (Q4) |
| Panics on a bad pattern | **change → `error`** | testable; the App reports all of them at once (Q1) |
| A read lock on every request; register while serving | **change → freeze** | no lock on the hot path (§7) |
| Lower-case method names in patterns (`"get /x"`) allowed | **change → rejected** | `"get /x"` can never match a real request, so it's always a typo |
| Host patterns, CONNECT handling, `GODEBUG` mode | **drop** for Milestone 2 | non-goals |
| (new) Path length and segment-count limits | **add**, returns 414 | bounds the work one request can cost (§6.7) |

## 4. Choosing the data structure

The question: given a list of route patterns, find the best one for a request path, quickly, with clear rules.

| Option | How it works | Verdict |
|---|---|---|
| **List of regexes** (many older frameworks) | try each route's regex in order | O(routes) per request; order-dependent ("first match" depends on registration order); ReDoS risk. **No** |
| **Hash map** `path → handler` | exact lookup | O(1), but can't do `{id}` at all. **Only works for static paths** |
| **Character radix tree** (httprouter, gin, echo) | each edge is a shared string prefix; `/user` and `/users` share `/user` | very fast and memory-efficient, but parameter and slash handling gets intricate, and edges split in the middle of words, so it's harder to reason about and to teach |
| **Segment trie** (ServeMux, chi in part) | each edge is one whole path segment | `{param}` can only ever match whole segments, which is exactly the rule we want; easy to read, test and prove things about. Slightly more nodes than radix |

**Decision: a segment trie.** Path parameters are defined in terms of segments, so a tree whose unit is the segment makes "a parameter can never match half a segment" true by construction, not by careful code. That matters more for a security-focused framework than the last few nanoseconds.

The radix idea is still useful as an **optimisation**: merging chains of single-child literal nodes (`api` → `v1` → `users` into one `api/v1/users` edge). It's listed as a refactor to try only if benchmarks show the extra levels cost something measurable (Q6).

### Example tree

Routes:

```
GET  /{$}
GET  /users
GET  /users/me
POST /users/new
GET  /users/{id}
GET  /users/{id}/posts
GET  /static/{rest...}
```

The trie (`◆` marks a node where a route ends; methods are stored at that leaf):

```
(root)
├── ""            ◆ GET /{$}          ← "" is the trailing-slash/empty segment
├── "users"       ◆ GET /users
│   ├── "me"      ◆ GET /users/me
│   ├── "new"     ◆ POST /users/new
│   └── {param}   ◆ GET /users/{id}
│       └── "posts" ◆ GET /users/{id}/posts
└── "static"
    └── {rest...} ◆ GET /static/{rest...}
```

**Looking up `GET /users/new`**, step by step:

1. root → literal child `"users"`
2. literal child `"new"` exists, and it's a leaf, but it only has **POST**. Note "POST is allowed here" (for a possible 405) and back up.
3. try the `{param}` child of `"users"`: it's a leaf with **GET**. Match: pattern `GET /users/{id}`, `id = "new"`.

The same answer `ServeMux` gave in §3.2.

## 5. Architecture

### 5.1 Packages

```
router/                     ← HTTP layer: what omolar.App uses
    router.go               Router, ServeHTTP, options
    errors.go               PatternError, ConflictError, ErrFrozen
    normalize.go            path validation, cleaning, limits  (§6.6–6.7)
    allow.go                Allow header building             (Quinze, task 2)
internal/routetree/         ← pure data structure, no net/http
    pattern.go              parse "GET /users/{id}" into segments (Quinze writes splitPath, task 1)
    tree.go                 node, insert, match
    conflict.go             relationship between two patterns
    walk.go                 Routes(): depth-first traversal   (Quinze, task 4)
```

**Why split it:** `routetree` is a generic `Tree[V any]`, a trie that stores any value V. Its tests use `V = string`, so they read like data-structure exercises ("insert these, look up that") with no HTTP noise. `router` puts `http.Handler` into it and adds the web-specific parts.

- **GO CONCEPT:** this is the first use of **generics** (`[V any]`) in Omolar.
- **PATTERN:** Separation of concerns / layering. The data structure has no idea what HTTP is, so it can be tested and changed on its own.

### 5.2 Pattern grammar

```
pattern  = [ method SP ] path
method   = 1*UPPER-TOKEN-CHAR          ; "GET", "POST", "PURGE"... upper case only
path     = "/" [ segment *( "/" segment ) ] [ "/" ]
segment  = literal | param | rest | end
literal  = 1*( any char except "/" "{" "}" "%" )   ; written unescaped: "/café"
param    = "{" name "}"
rest     = "{" name "...}"             ; last segment only
end      = "{$}"                        ; last segment only; means "exactly a trailing slash"
name     = Go identifier, unique within the pattern
```

Rejected at registration, each with a `*PatternError` giving the position:

- empty pattern; missing leading `/`; a lower-case or invalid method
- empty segments (`/a//b`); `.` or `..` literals (unreachable after cleaning)
- a wildcard that's only part of a segment (`/file{id}`); `{}` or `{...}` with no name; names that aren't Go identifiers
- duplicate names (`/{id}/{id}`); `{rest...}` or `{$}` anywhere but last
- `%` in a literal (ambiguous: is `%41` the text "%41" or "A"?)

**Splitting rule:** `"/"` is one empty segment `[""]`; `"/a/b"` is `["a","b"]`; `"/a/b/"` is `["a","b",""]`. A trailing slash becomes a final empty segment, and a pattern ending in `/` or `/{$}` means exactly that. With this rule, "trailing slash" needs no special case in the tree.

### 5.3 Node structure

```go
// internal/routetree/tree.go (sketch, not final)

type Tree[V any] struct {
    root   node[V]
    routes []*route[V] // every route, for conflict checks and Routes()
}

type node[V any] struct {
    // Literal children, sorted by label so lookup can binary-search.
    // labels[i] is the segment text for statics[i]. Two parallel slices,
    // not a slice of structs, so the search loop only touches the labels.
    labels  []string
    statics []*node[V]

    param *node[V] // at most one {name} child; the names live on the route
    rest  *route[V] // at most one {name...}: always ends a route

    leaf *leafRoutes[V] // routes that end exactly here; nil for interior nodes
}

type leafRoutes[V any] struct {
    byMethod []methodRoute[V] // small: a handful of methods, so a linear scan beats a map
    any      *route[V]        // registered with no method
}

type methodRoute[V any] struct {
    method string
    route  *route[V]
}

type route[V any] struct {
    pattern    string   // as registered, e.g. "GET /users/{id}"
    method     string   // "" = any method
    segments   []segment
    paramNames []string // in order: values are matched by position
    value      V
}
```

Notes:

- **Parameter names live on the route, not on the node.** `/users/{id}` and `/users/{uid}/posts` share one `{param}` child; each route says what its own parameters are called. (ServeMux does this too.)
- **Literal children are a sorted slice with binary search**, not a map. Typical nodes have 1–10 children, where a short binary search beats hashing and allocates nothing. The benchmark plan (§11.8) compares it against a map and against ServeMux's hybrid at 50 children. If a map wins at high fan-out, we copy ServeMux's switch-over. **Decided by measurement, not guesswork.**
- **At most one `param` and one `rest` child per node.** That's what keeps backtracking bounded (§9).

### 5.4 Matching algorithm

Matching runs in two phases. They're separate on purpose:

**Phase 1: normalise and validate** (`router/normalize.go`, once per request, O(path length)). This takes `r.URL.EscapedPath()` and:

1. applies the limits: path length, and segment count, which is a 414 if exceeded
2. cleans it. If cleaning changed it, the result is a 307 for GET/HEAD and a 400 for other methods
3. splits it into segments, using a fixed-size array on the stack (`[32]string`), falling back to the heap only past 32
4. unescapes each segment, but only if it contains `%`, so most segments are never copied
5. rejects a segment whose decoded form has `/`, is `.` or `..`, contains NUL or a control character, or isn't valid UTF-8 (400)

**Phase 2: match** (`routetree`, recursive depth-first search):

```
match(node, segments, method):
    if no segments left:
        if node.leaf has a route for method          → return it
        if method is HEAD and node.leaf has GET      → return GET route
        if node.leaf has an any-method route         → return it
        if node.leaf is non-nil                      → record its methods as "allowed"
        return no match

    seg := segments[0]
    1. if node has a literal child == seg:       r := match(child, rest, method);       if r → return r
    2. if node.param != nil and seg != "":       r := match(node.param, rest, method);  if r → return r   (record seg as a param value)
    3. if node.rest != nil and it fits method:   return it   (value = all remaining segments joined by "/")
    return no match
```

When the search fails, the router answers 405 with the recorded allowed methods, or 404 if nothing was recorded. A literal can be tried before a wildcard because conflicting patterns were refused earlier. Here's why the first route found is the most specific one, which is worth understanding:

> Score each segment of a pattern: literal = 3, `{param}` = 2, `{rest...}` = 1. Pattern A is *more specific* than B if A's score is ≥ B's at every position and > at one or more. Any two routes that could match the same request must be comparable this way, or registration would have rejected them as a conflict. So among all routes matching a request, one of them is ≥ all the others in every position. The search tries literal, then param, then rest at each step, so it visits patterns in descending order of score from left to right ("lexicographic" order). The pattern that's ≥ everything at every position comes first in that order. **So the first match found is the most specific.** It's the same argument the ServeMux source refers to. Methods fit into this too: they're checked at the leaf, and conflict detection compares method and path together, so two routes that both match a request still can't tie.

### 5.5 Parameter extraction

- During matching, values go into a small array owned by the match call (`[8]string`, heap only past 8 parameters), recorded by position.
- On success, the router pairs each value with the route's `paramNames` and calls `r.SetPathValue(name, value)`. It also sets `r.Pattern = route.pattern`, which Milestone 3's request logging will use, because it's low-cardinality and has no IDs in it.
- `{rest...}` receives the remaining **decoded** segments joined with `/`. That's safe because phase 1 already rejected any decoded `/`, `.` or `..`.
- A trade-off we measured: `SetPathValue` costs **2 allocations, 336 bytes** per request with parameters (go1.27.4). `ServeMux` avoids it by writing unexported request fields an outside router can't reach. The alternative, putting parameters in the request context, costs more (`r.WithContext` copies the whole request). We accept it, so handlers work unchanged with `r.PathValue`.

## 6. Routing rules

### 6.1 Precedence (most specific wins)

1. Compare segment by segment, left to right: literal > `{param}` > `{rest...}`.
2. If two routes have the same path shape: an exact method match beats HEAD-served-by-GET, which beats method-less.
3. Registration order **never** matters. Registering A then B, or B then A, gives the same router. That gets its own test (§11.2).

| Routes | Request | Winner |
|---|---|---|
| `/users/me`, `/users/{id}` | `/users/me` | `/users/me` |
| `/a/b/z`, `/a/{x}/c` | `/a/b/c` | `/a/{x}/c`, found by backtracking |
| `/static/{rest...}`, `/static/{name}` | `/static/app.js` | `/static/{name}` |
| `/static/{rest...}`, `/static/{name}` | `/static/css/app.css` | `/static/{rest...}` |
| `GET /x`, `/x` | `GET /x` / `PUT /x` | `GET /x` / `/x` |

### 6.2 Conflicts

Two routes **conflict** when some request matches both and neither is more specific. Rejected at registration with a `*ConflictError` that names both patterns **and an example path that matches both**, so the fix is obvious.

| Existing | New | Result |
|---|---|---|
| `GET /a/b` | `GET /a/b` | conflict: duplicate |
| `GET /u/{id}` | `GET /u/{name}` | conflict: same shape, different name |
| `GET /a/{x}` | `GET /{y}/b` | conflict: both match `/a/b` |
| `GET /a/{x}` | `POST /{y}/b` | OK: no request has both methods |
| `/x` (any method) | `GET /{y}` | conflict: `GET /x` matches both |
| `GET /files/{p...}` | `GET /files/{name}` | OK: `{name}` is more specific |
| `GET /a/` | `GET /a/{$}` | conflict: identical meaning |

Checking a new route compares it against every existing route: O(routes × segments). This only happens at startup (§9).

### 6.3 Wildcards

| Form | Matches | Value | Notes |
|---|---|---|---|
| `{name}` | exactly one **non-empty** segment | that segment, decoded | never matches a trailing slash |
| `{name...}` | the rest of the path: zero or more segments | rest joined by `/`, decoded | must be last; `"/static/{p...}"` matches `/static/` (`p=""`) but not `/static` |
| `{$}` | only an empty final segment (a trailing slash) | none | `"/{$}"` = the root page only |

### 6.4 Methods

- A route may name one method or none (none = any method).
- `HEAD` uses the `GET` route when there's no explicit `HEAD` route; `net/http` drops the body for us.
- If the path matches routes but none for this method: **405 Method Not Allowed** with `Allow:` listing the methods that would have worked, sorted and with no duplicates, plus `HEAD` if `GET` is there. (Quinze, task 2.)
- `OPTIONS` isn't answered automatically in Milestone 2. CORS middleware in Milestone 3 owns it (Q5).
- Methods are case-sensitive. A request with `get` gets 405, the same as ServeMux.

### 6.5 Trailing slashes

- A pattern ending in `/` (or `/{$}`) matches **only** that exact path with the slash. **This is different from ServeMux**, where `"/files/"` matches the whole subtree. In Omolar a subtree is spelled out: `"/files/{path...}"`. Reading the route tells you exactly what it catches.
- **Redirects:** when nothing matches, a GET/HEAD request is retried once with the trailing slash added or removed (ServeMux only ever adds one). If that matches for the same method, the router answers 307 to it, keeping the query string. Other methods get no redirect, just the 404/405. This can be turned off with an option. (Quinze writes this feature's tests, task 3.)

### 6.6 Path cleaning and redirects

- Phase 1 cleans the escaped path like ServeMux (`path.Clean`, keeping a trailing slash).
- If cleaning changed it: GET/HEAD get **307** to the cleaned path, keeping the query string; other methods get **400** (Q3).
- **The `Location` header is always built from the cleaned path, never from `Host` or anything else the client sent.** A cleaned path starts with exactly one `/`, so it can't become `//evil.com`, which browsers read as "go to evil.com". A backslash is escaped as `%5C` in `Location`, because some browsers treat `/\evil.com` the same way. Both get security tests.

### 6.7 Inputs refused before matching

| Input | Response | Why |
|---|---|---|
| Decoded segment containing `/` (`%2F`) | 400 | §3.3; opt-in option to allow it in `{param}` only (Q4) |
| Decoded segment `.` or `..` (`%2e`, `%2e%2e`) | 400 | the traversal bypass from §3.3 |
| `%00` or control characters after decoding | 400 | NUL truncation in C libraries and log injection (`\r\n`) |
| Invalid UTF-8 after decoding | 400 | no two byte sequences that "look the same" (Q7) |
| Path longer than `MaxPathLength` (default 8 KiB) | 414 | bounds the work one request can cause |
| More than `MaxSegments` (default 128) | 414 | same; also bounds how deep the matching recursion goes |

## 7. Concurrency safety

**Model: build, then freeze.**

1. During setup, `Handle` adds routes while holding a `sync.Mutex`, and checks `frozen` **while holding it**. Concurrent setup code is safe, and this is slow-path only.
2. Each `ServeHTTP` starts with `if !frozen.Load()`. Only while the router isn't frozen yet (in practice, the very first request) does it take the same mutex and set `frozen.Store(true)`. From then on `Handle` returns `ErrFrozen`, and `omolar.App` turns that into a startup error.
3. After freezing, the tree is **never written again**. Any number of goroutines read an unchanging structure at once, and the hot path costs one atomic load and no lock.

**Why taking the mutex to freeze matters.** Without it, there's a race. `Handle` checks `frozen == false`, starts inserting nodes, and at that moment the first request sets `frozen` and starts reading the half-written tree. Because the freeze takes the registration mutex, it must wait for any `Handle` call in progress to finish. And any `Handle` that runs later sees `frozen == true` under the lock and changes nothing.

**Why it's safe in Go's memory model.** A mutex unlock "happens before" the next lock, and an atomic store happens before any load that observes it. So: every tree write (under the lock) → the freeze (takes the lock, stores `true`) → every request that loads `frozen == true` and reads the tree. Each write is guaranteed visible to each reader. In the normal case, all routes are registered before `server.Serve` starts any goroutine, and the `go` statement creates the same guarantee anyway; the freeze is what makes the rare "late `Handle`" case safe too. The race detector (`-race`) checks all of this in §11.6.

**Why not do what ServeMux does?** ServeMux takes a read lock on every request so routes can be added while serving. Uncontended read locks are cheap but not free: the atomic counter becomes a contended cache line under parallel load. And adding routes mid-flight is a feature nobody in Omolar needs, which also makes the route table harder to audit. The alternative if live reload is ever needed: **copy-on-write**, meaning build a new tree and swap it in with `atomic.Pointer[tree]`. Readers never block, and old trees are freed by the garbage collector once no request uses them. This is noted as a future option, not built.

## 8. Memory and allocations

**Per request:**

| Step | Allocates? |
|---|---|
| Reading `r.URL.EscapedPath()` | no (it's usually already there) |
| Splitting into segments | **no**: segments are substrings of the path (they share its bytes) and live in a `[32]string` array on the stack |
| Unescaping a segment | only if that segment contains `%` |
| Matching (recursion, binary search) | **no** |
| Collecting parameter values | **no**: `[8]string` on the stack |
| `r.SetPathValue` | **yes**: 2 allocations, 336 bytes, routes with parameters only (§5.5) |
| 405 `Allow` header | yes, but it's an error path |

**Targets, checked by benchmarks with `b.ReportAllocs`:**

| Route type | Allocations per request |
|---|---|
| static (`/api/v1/status`) | **0** |
| with parameters | **≤ 2** (the `SetPathValue` cost) |
| 404 | **0**, apart from what `http.NotFound` writes |

**Baseline**, ServeMux on this 2-core sandbox (go1.27.4, 100 routes):

| Route | Time | Memory | Allocations |
|---|---|---|---|
| static | 199 ns/op | 0 B | 0 |
| 1 parameter | 248 ns/op | 16 B | 1 |

Our numbers get measured the same way, and both appear in the Milestone 2 PR whatever they turn out to be.

**"On the stack" isn't something Go promises.** The compiler's *escape analysis* decides whether a variable can live on the stack, which is free, or must go to the heap, where the garbage collector manages it. If a pointer to the `[32]string` array escaped (say, stored in a struct that outlives the call), the array would silently move to the heap. We check with `go build -gcflags=-m ./router` and keep the output in the PR. A benchmark showing 0 allocations is the real proof.

**Not using `sync.Pool`, for now.** A pool of reusable buffers would only help if allocations remain, and pools make memory ownership harder to reason about. We'll reach for it only if a benchmark asks.

**Registration** allocates freely (nodes, slices, route records). It happens once at startup.

## 9. Algorithmic complexity

`k` = segments in the request path, `L` = path length in bytes, `R` = registered routes, `N` = nodes in the trie (≤ total segments across all routes), `c` = literal children at one node.

| Operation | Cost | Notes |
|---|---|---|
| Phase 1: normalise and validate | **O(L)** | one pass, plus `path.Clean` (also O(L)) |
| Phase 2: match, typical | **O(k · log c)** | one binary search per segment |
| Phase 2: match, worst case | **O(N · log c)** | see below |
| Insert one route | **O(k · c)** | inserting into a sorted slice shifts elements |
| Conflict check for one new route | **O(R · k)** | compares against every route |
| Registering all routes | **O(R² · k)** | startup only: 1,000 routes × 10 segments ≈ 10M simple steps, a few milliseconds. Benchmarked |
| Memory | **O(N)** nodes, plus O(R) route records | |

**Why backtracking can't explode.** Backtracking can sound exponential: with three choices per level, `3^k`. It isn't, because **the trie is a tree**. Each branch the search abandons is a different subtree, and no node is ever visited twice in one lookup, so the most work a search can ever do is visiting every node once: O(N). That's an upper bound for deliberately strange route sets; real ones backtrack once or twice. `MaxSegments` caps `k`, which caps how deep the recursion can go.

**Compared with the alternatives:** a regex list is O(R · L) per request, and potentially exponential with bad regexes; a hash map is O(L) but static-only; a radix tree is O(L) with smaller constants. A segment trie sits at O(k log c) with clear semantics.

## 10. Public API and `omolar.App` integration

### 10.1 `router` package

```go
package router

// New creates an empty router. Options adjust limits and handlers.
func New(opts ...Option) *Router

// Handle registers h for pattern, e.g. "GET /users/{id}".
// It returns a *PatternError for a malformed pattern, a *ConflictError if
// the pattern is ambiguous with an existing one, or ErrFrozen after the
// router has started serving.
func (rt *Router) Handle(pattern string, h http.Handler) error
func (rt *Router) HandleFunc(pattern string, fn func(http.ResponseWriter, *http.Request)) error

// ServeHTTP routes a request (Router is an http.Handler).
// The first call freezes the router.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request)

// Routes lists every registered route, sorted. (Quinze, task 4.)
func (rt *Router) Routes() []Route

// Match reports which route a request would reach, without serving it.
// For tests, debugging and the startup route log.
func (rt *Router) Match(method, path string) (Match, bool)

type Route struct {
    Method  string // "" = any
    Pattern string
}

type Match struct {
    Pattern string
    Params  []Param // in pattern order
}

type Param struct{ Name, Value string }

// Options (functional options, as in Milestone 1):
func WithNotFound(h http.Handler) Option
func WithMethodNotAllowed(h http.Handler) Option   // the Allow header is set before h runs
func WithTrailingSlashRedirect(on bool) Option      // default true
func WithMaxPathLength(n int) Option                // default 8192
func WithMaxSegments(n int) Option                  // default 128
func WithEncodedSlashInParams(allow bool) Option    // default false (Q4)

// Errors
type PatternError struct { Pattern string; Offset int; Reason string }
type ConflictError struct { New, Existing string; Example string }
var ErrFrozen = errors.New("router: routes cannot be added after the router starts serving")
```

**A deliberate omission:** no `Get(path, h)` / `Post(path, h)` helper methods yet. `"GET /path"` already says it, and one way to register routes is easier to audit. They're easy to add later if wanted.

### 10.2 What changes in `omolar.App`

| | Milestone 1 | Milestone 2 |
|---|---|---|
| Router | `*http.ServeMux` | `*router.Router` |
| `app.Handle`, `app.HandleFunc` | same signatures | same signatures; a bad or conflicting pattern is **recorded** (§10.3) |
| `/health` | registered as `"/health"` (any method; the handler itself returns 405) | `"GET /health"`: the router handles HEAD and 405, and the handler's own check stays as a second line of defence |
| Startup log | "server started" | also logs the route table (`Routes()`), one line per route, at debug level |
| `r.PathValue`, `r.Pattern` | set by ServeMux | set by the Omolar router: handlers don't change |

All Milestone 1 patterns (`"GET /{$}"`, `"GET /hello/{name}"`, `"GET /teapot"`) are valid unchanged. The existing tests become regression tests for the switch.

### 10.3 Registration errors in the App (recommended, see Q1)

`app.HandleFunc` keeps its signature (no error return), so app code stays tidy:

1. Each failed registration is appended to a list inside the App, the same "Notification" pattern `config.Validate` uses.
2. `app.Run` and `app.Serve` refuse to start while the list is non-empty, and return **every** problem at once.
3. `app.Err()` exposes the list, so tests and `main()` can check right after registering.

The alternative is panicking like ServeMux. Familiar, but a library panicking in your `main()` is abrupt, and it stops at the first problem.

## 11. Test plan

All tests are written first, per CLAUDE.md. Each group below becomes a red commit, then a green one.

### 11.1 Pattern parsing (`routetree`)

A table of valid patterns and what they parse into (method, segments, kinds, names). A table of invalid ones, each with the expected `Offset` and `Reason`: every rule in §5.2. `splitPath` gets its own table: `"/"`, `"/a"`, `"/a/"`, `"/a/b/c"`, plus rejection of `"//"` and `"/a//b"`.

### 11.2 Tree behaviour (`routetree`, `V = string`)

- Insert-then-match tables for every row of §6.1 and §6.3.
- **Backtracking cases:** `/a/b/z` + `/a/{x}/c` against `/a/b/c`; a three-level backtrack.
- **Parameters by position:** `/{a}/x/{b}` against `/1/x/2` → `["1","2"]`; two routes sharing a `{param}` node with different names.
- **Order independence:** register a route set in 50 random orders (with a fixed seed so failures reproduce) and check every lookup gives the same result.
- Empty tree; root only; `{rest...}` matching zero segments; `{$}` versus no slash.

### 11.3 Conflicts (`routetree`)

Every row of §6.2 plus the ServeMux results in §3.2, checking the error type, both pattern names, and that `Example` really matches both patterns. Also: a rejected route must leave the tree unchanged (no half-inserted nodes).

### 11.4 HTTP behaviour (`router`)

- 200 with the right `r.PathValue` and `r.Pattern`; a custom `WithNotFound`.
- **405:** `Allow` sorted, without duplicates, with HEAD; method fallback (`GET /users/new` → `{id}`); method-less routes.
- **HEAD:** served by GET with no body; an explicit HEAD route wins.
- **Trailing-slash redirects** (Quinze writes these): 307 for GET/HEAD; query string kept; nothing for POST; switched off by the option; never a redirect loop.
- **Cleaning:** `//a`, `/a/./b`, `/a/../b` → 307 for GET, 400 for POST.
- **Freeze:** `Handle` after the first request → `ErrFrozen`.

### 11.5 Security edge cases (`router`, one test each)

| Case | Expected |
|---|---|
| `/static/a%2F..%2F..%2Fetc%2Fpasswd` (the §3.3 bypass) | 400, handler never called |
| `/users/a%2Fb` | 400 by default; 200 with `id="a/b"` with the option on |
| `/users/%2e%2e`, `/users/%2E`, `/users/.%2e` | 400 |
| `/a%00b`, `/a%0d%0aSet-Cookie:x` | 400 |
| `/caf%C3%A9` (valid UTF-8) | 200 if `/café` is registered |
| `/%C0%AF` (overlong UTF-8 encoding of `/`) | 400 |
| Path that cleans to `//evil.com` | `Location` never starts with `//` |
| `/\evil.com` | `Location` uses `%5C` |
| 8 KiB + 1 path / 129 segments | 414 |
| `/Admin` when only `/admin` is registered | 404, never matched case-insensitively |
| Request with method `get` | 405 |
| A raw `..` segment that net/http hasn't cleaned (built in the test) | cleaned by phase 1 |

### 11.6 Concurrency (`router`, run with `-race`)

- 100 goroutines × 1,000 requests across all route types against a frozen router: no races, every answer correct.
- `Handle` from several goroutines during setup: all routes present, no race.
- `Handle` racing the first `ServeHTTP`: either the route is added or `ErrFrozen` comes back, never a corrupted tree.

### 11.7 Fuzzing (Go native fuzzing, `go test -fuzz`)

- **`FuzzParsePattern`:** random strings never panic. Anything that parses can be re-printed and parsed again to the same result ("round-trips").
- **`FuzzMatch`:** random paths against a fixed route set never panic. Any 200 came from a registered pattern. Filling the returned parameters into the pattern rebuilds a path that cleans to the same thing.
- **`FuzzAgainstServeMux` (differential):** register the same route set, avoiding the documented differences (§3.4), in both ServeMux and Omolar; fuzz request paths; both must pick the same pattern, or both 404/405. **This uses the standard library as a reference to check us against.** Where they disagree outside the documented differences, ours is wrong.
- Seed corpora include every security case from §11.5. Each fuzz target also runs as an ordinary test over its corpus in `make test` and CI, and runs for real with `-fuzz` on demand (`make fuzz`).

### 11.8 Benchmarks (`b.Loop`, `b.ReportAllocs`, each against ServeMux on the same machine)

| Benchmark | Shape |
|---|---|
| `Static` | `/api/v1/status` among 100 routes |
| `Param1` / `Param3` | `/users/{id}`, `/orgs/{org}/repos/{repo}/issues/{n}` |
| `Rest` | `/static/{p...}` with 5 trailing segments |
| `Deep` | 10 literal segments |
| `Backtrack` | the `/a/{x}/c` case |
| `NotFound` / `MethodNotAllowed` | error paths |
| `GitHubAPI` | ~250 real-world-shaped routes (GitHub's REST layout), a mix of lookups |
| `Parallel` | `b.RunParallel` over `GitHubAPI` (where avoiding a lock should show) |
| `Register1000` | building the table, checking §9's startup cost estimate |
| `StaticChildren` (internal) | sorted slice vs map vs hybrid at 4, 16 and 64 children: decides §5.3 |

Allocation targets are pass/fail (§8). For speed the target is "competitive with ServeMux", and the real numbers are reported in the PR either way.

## 12. How this maps to C and C++

Coming from C and C++, here's how each part of this design would look there, and what Go does differently.

| Concept here | C / C++ | What's different in Go |
|---|---|---|
| `*node[V]` child pointers | `struct node *child;` / `std::unique_ptr<Node>` | **No manual freeing.** The garbage collector frees a node once nothing points to it, so no `free`/`delete`, no use-after-free, no double-free. Pointers still exist, but there's **no pointer arithmetic** |
| `nil` | `NULL` / `nullptr` | Calling a method on a nil pointer is legal if the method checks for nil. ServeMux's `matchPath` starts with `if n == nil` for exactly this reason |
| `[]*node[V]` slice | `std::vector<Node*>` | A slice is a 3-word header (pointer, length, capacity), copied by value. `append` may reallocate like `vector::push_back`, but an old slice keeps pointing at the old array: no undefined behaviour from stale iterators |
| Segments as substrings | `std::string_view` / `char*` + length into the request buffer | Go strings are immutable, so a substring safely **shares** the original bytes: zero-copy splitting. The catch: a tiny substring keeps the whole original string alive for the GC. Fine for one request's lifetime; it matters if you cache substrings |
| `[32]string` on the stack | `std::array<std::string_view, 32>` | In C++ **you** choose stack or heap. In Go the **compiler's escape analysis** chooses, and `-gcflags=-m` shows its decision |
| Sorted children + binary search | `std::lower_bound` over a sorted vector | Same algorithm (`slices.BinarySearch` / `sort.Search`). Same reason too: cache-friendly contiguous memory beats pointer-chasing in a `std::map` (a red-black tree) |
| `map[string]*node` (the alternative) | `std::unordered_map` | Go maps are hash tables with a per-map random seed (defends against hash-flooding attacks) and random iteration order. You **can't** take the address of a map element (`&m[k]` won't compile), because entries move when the map grows; in C++ references into `unordered_map` stay valid |
| Recursive `match` | recursive function | Goroutine stacks start small (a few KB) and **grow automatically**, so deep recursion doesn't segfault the way a fixed C stack overflows. It still costs memory, hence `MaxSegments` |
| `Tree[V any]` | `template <typename V> class Tree` | Go generics compile one copy of the code per "GC shape" (all pointer types share one), plus a dictionary, rather than one copy per type like C++ templates. Less code bloat, sometimes slightly slower. There's no template metaprogramming |
| `atomic.Bool` freeze | `std::atomic<bool>` with release/acquire | Same idea, the same "publish an immutable structure" pattern. Go's atomics are sequentially consistent (the strongest ordering), with no `memory_order_relaxed` to get subtly wrong |
| `sync.Mutex` for registration | `std::mutex` / `pthread_mutex_t` | No RAII, but `defer mu.Unlock()` does the job |
| Index out of range | **undefined behaviour**: a buffer over-read or overflow | **A panic**, always checked. Parsing hostile URLs in Go can't silently read past a buffer. That whole class of C vulnerability is gone, which matters to a security engineer |
| Struct field order | affects `sizeof` through padding | Same in Go (`unsafe.Sizeof` shows it). Hot fields (`labels`, `statics`) go first so one cache line covers the search |

## 13. Work plan and who builds what

Commits follow red → green from CLAUDE.md. **Q** = Quinze writes it; Claude writes the failing tests (or, for task 3, the rules) and reviews the code.

| # | Step | Who |
|---|---|---|
| 1 | `routetree`: pattern parsing tests; **`splitPath` (task 1)** | tests: Claude · code: **Q** |
| 2 | `routetree`: rest of the pattern parser | Claude |
| 3 | `routetree`: node, insert, match, precedence, backtracking | Claude |
| 4 | `routetree`: conflict detection with example paths | Claude |
| 5 | `routetree`: **`Routes()` depth-first walk (task 4)** | tests: Claude · code: **Q** |
| 6 | `router`: normalise/validate phase, limits, security cases | Claude |
| 7 | `router`: ServeHTTP, 404, freeze, options | Claude |
| 8 | `router`: **`Allow` header for 405 (task 2)** | tests: Claude · code: **Q** |
| 9 | `router`: trailing-slash redirect. **Tests (task 3)** | rules: Claude · tests: **Q** · code: Claude |
| 10 | `omolar.App` switches to the router; registration errors; route log | Claude |
| 11 | Fuzz targets, including the ServeMux differential | Claude |
| 12 | Benchmarks, escape-analysis check, results in the PR | Claude |
| 13 | `docs/learning/02-router.md` | Claude |

Each Q task ships as a task card in `docs/tasks/`: goal, the tests to make pass, the concepts needed, and hints 1–3 on request. Claude's steps that depend on a Q task (5 builds on 3; 9's tests come before its code) wait for it, but other steps can carry on in parallel.

## 14. Open questions for review

Each has a recommendation. Say "agree" or pick the other option.

| # | Question | Recommendation | Alternative |
|---|---|---|---|
| Q1 | Registration errors in `App` | **Collect, `app.Err()`, refuse to start** (§10.3) | panic like ServeMux |
| Q2 | Trailing-slash patterns | **Exact match; subtree must be `{rest...}`** | ServeMux's implicit subtree |
| Q3 | Non-canonical path on POST/PUT/... | **400** | 307 redirect like ServeMux |
| Q4 | `%2F` inside a `{param}` | **400 by default**, with an opt-in option | always allow (ServeMux) |
| Q5 | Automatic `OPTIONS` responses | **No**: Milestone 3's CORS middleware | answer with `Allow` automatically |
| Q6 | Radix compression of literal chains | **Defer; only if benchmarks show a cost** | build it now |
| Q7 | Invalid UTF-8 in a decoded segment | **400** | pass it through as bytes |
| Q8 | `Get`/`Post` helper methods | **Not now** | add them |

Once these are settled, the next step is step 1: the pattern-parsing tests and the `splitPath` task card.
