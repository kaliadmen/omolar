# Design: Milestone 2 router

| | |
|---|---|
| **Status** | Revision 2. Architecture approved; review decisions and corrections applied (§15). Implementation not started |
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
14. [Review decisions](#14-review-decisions)
15. [Revision history](#15-revision-history)

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
| `{rest...}` | Matches the remainder **after a slash**: `/static/{p...}` matches `/static/` (`p=""`) and `/static/a/` (`p="a/"`), never `/static` (§5.4.1) |
| Unsafe paths | `%2F`, `%2e%2e`, `%00`, control characters and invalid UTF-8 in a segment get **400**, with no opt-out. Over-long paths get **414** (§6.7) |
| Non-canonical paths | Cleaned and redirected (307) for GET/HEAD; **400** for every other method |
| Filesystem safety | **Not the router's job.** URL checks are defence in depth only; file access needs its own safeguards (§6.8) |
| Concurrency | **Build, then freeze.** `Router.Freeze()` is called explicitly by `omolar.App` at startup. A standalone router freezes itself on its first request. Lookups take no locks (§7) |
| Path values | Set with `r.SetPathValue`, so handlers use the standard `r.PathValue("id")`. `r.Pattern` is set too |
| Allocations | Correctness first. Static routes allocate nothing; routes with parameters allocate exactly what `SetPathValue` needs, and nothing more (§8) |
| Registration errors | `router.Router.Handle` returns them directly (the checked API). `omolar.App` collects them and refuses to start, listing all of them (§10.3) |
| `OPTIONS` | Only explicitly registered routes. Nothing automatic |
| Deferred | Radix compression, `Get`/`Post` helpers, encoded-slash opt-in, host routing |
| Dependencies | None (standard library only) |

## 2. Goals and non-goals

**Goals**

- **Correct:** for a given request, exactly one route matches, or a well-defined 404/405/redirect/400 is returned. No route order surprises.
- **Secure by default:** the router never hands a handler a path value that means something different from what path cleaning checked (§3.3). It doesn't claim to make file access safe (§6.8).
- **Fast enough, and measured:** no allocations the design doesn't need (§8). Benchmarked against `ServeMux` on the same hardware.
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

has a path traversal bug. **The bug is in the handler's file access, and that's where it has to be fixed.** Still, a security-minded router shouldn't make it easy. **Omolar's rule: a path value a handler receives must never contain a separator or a dot segment that the cleaning step didn't see.** Concretely (§6.7): a decoded `/`, `.` or `..` segment, `%00`, a control character, or invalid UTF-8 inside any segment gets 400.

That rule is **defence in depth for URLs only**. It does **not** make `filepath.Join(root, value)` safe, and handlers must not rely on it. §6.8 explains why, and treats file access as its own design problem.

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
| Decoded `/` and dot segments reach handlers | **change → 400, no opt-out in Milestone 2** | §3.3 (Q4) |
| Invalid UTF-8 in a segment passed through | **change → 400** | (Q7) |
| Panics on a bad pattern | **change → `error`** | testable; the App reports all of them at once (Q1) |
| A read lock on every request; register while serving | **change → explicit `Freeze()`** | no lock on the hot path; the App freezes at startup (§7) |
| No automatic `OPTIONS` | **keep** | only registered `OPTIONS` routes answer it (Q5) |
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

    // Routes whose LAST segment is {name...} at this position. It's a method
    // table, like leaf, because "GET /files/{p...}" and "PUT /files/{p...}"
    // share this slot. A {rest...} never has children: it's always last.
    rest *leafRoutes[V]

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
- **At most one `param` child and one `rest` table per node.** That's what keeps backtracking bounded (§9).
- **The `leaf` and `rest` tables have the same type** on purpose: method selection (exact, then HEAD→GET, then method-less) is one function used in both places, so the rules can't drift apart.

### 5.4 Matching algorithm

Matching runs in two phases. They're separate on purpose:

**Phase 1: normalise and validate** (`router/normalize.go`, once per request, O(path length)). This takes `r.URL.EscapedPath()` and:

1. applies the limits: path length, and segment count, which is a 414 if exceeded
2. cleans it. If cleaning changed it, the result is a 307 for GET/HEAD and a 400 for other methods
3. splits it into segments, using a fixed-size array on the stack (`[32]string`), falling back to the heap only past 32
4. unescapes each segment, but only if it contains `%`, so most segments are never copied
5. rejects a segment whose decoded form has `/`, is `.` or `..`, contains NUL or a control character, or isn't valid UTF-8 (400)

**Phase 2: match** (`routetree`, recursive depth-first search). `segs` is the list of segments still to match; a trailing slash is a final `""` segment (§5.2):

```
match(node, segs, method, m):          # m holds the param values found so far + the "allowed" set
    if len(segs) == 0:
        return pick(node.leaf, method, m)                 # the path ends exactly here

    seg, tail := segs[0], segs[1:]

    # 1. literal child (this includes the "" child created by a trailing-slash or {$} pattern)
    if child := node.literal(seg); child != nil:
        if r := match(child, tail, method, m); r != nil: return r

    # 2. single wildcard: one NON-EMPTY segment
    if node.param != nil and seg != "":
        m.push(seg)
        if r := match(node.param, tail, method, m); r != nil: return r
        m.pop()                                            # undo: the abandoned branch's value must not leak

    # 3. terminal {rest...}: takes ALL remaining segments, len(segs) >= 1 guaranteed here
    if node.rest != nil:
        if r := pick(node.rest, method, m); r != nil:
            m.push(join(segs, "/"))                        # "" for [""], "a/" for ["a",""], "a/b" for ["a","b"]
            return r
    return nil

pick(table, method, m):                # same rules for an ordinary leaf and for a {rest...} table
    if table == nil:                       return nil
    if r := table.byMethod[method]:        return r
    if method == HEAD and table has GET:   return GET route
    if table.any != nil:                   return table.any
    m.allowed.add(all methods in table)    # path matched, method didn't: remember for 405
    return nil
```

When the whole search fails, the router answers **405** with `Allow` built from `m.allowed` if it's non-empty, otherwise **404**. A failed search has visited every branch that could match the path, so `m.allowed` is complete. It's never filled when the search succeeds, so that costs nothing on the happy path.

#### 5.4.1 Terminal `{rest...}`, exactly

Two rules settle every case:

1. **`{rest...}` is only tried while at least one segment remains** (`len(segs) >= 1`), so it always comes *after* a slash. With `GET /static/{p...}`:
   - `/static` → segments `["static"]`. Nothing remains after `static`, so `{p...}` isn't tried → no match here. A GET then gets the trailing-slash redirect to `/static/` (§6.5); other methods get 404.
   - `/static/` → `["static", ""]`. One segment remains (the empty one) → match, `p = ""`.
2. **The value is the remaining segments joined with `/`, so a trailing slash is kept** as a trailing `/`.

| Pattern | Request | Remaining segments | Result |
|---|---|---|---|
| `/static/{p...}` | `/static` | `[]` | no match (GET: 307 → `/static/`) |
| `/static/{p...}` | `/static/` | `[""]` | match, `p = ""` |
| `/static/{p...}` | `/static/a` | `["a"]` | `p = "a"` |
| `/static/{p...}` | `/static/a/` | `["a", ""]` | `p = "a/"` |
| `/static/{p...}` | `/static/a/b` | `["a", "b"]` | `p = "a/b"` |
| `/{p...}` | `/` | `[""]` | `p = ""` (root catch-all) |
| `/{p...}` | `/x/y/` | `["x", "y", ""]` | `p = "x/y/"` |
| `/files/{p...}` + `/files/{$}` | `/files/` | `[""]` | `/files/{$}`: the literal `""` beats rest |
| `/files/{p...}` + `/files/{name}` | `/files/a` | `["a"]` | `/files/{name}`: param beats rest |
| `/files/{p...}` + `/files/{name}` | `/files/a/` | `["a", ""]` | `/files/{p...}`, `p = "a/"`: `{name}` matches `a`, but nothing under it matches `""`, so the search backtracks to rest |
| `GET /files/{p...}` + `PUT /files/{name}` | `GET /files/a` | `["a"]` | `{name}` has only PUT (recorded as allowed) → backtrack → `GET /files/{p...}`, `p = "a"` |
| `GET /files/{p...}` + `PUT /files/{name}` | `PUT /files/a/b` | `["a", "b"]` | `{name}` doesn't fit two segments; rest has only GET → **405, `Allow: GET, HEAD`** |

For GET, every row above matches what `http.ServeMux` does in go1.27.4 (checked by running it). For other methods Omolar never redirects (§6.5). Each row becomes a test (§11.2).

**Interior empty segments never reach the matcher.** `/a//b` is non-canonical, so phase 1 redirects or rejects it (§6.6). The only empty segment `match` can ever see is the final one. A literal can be tried before a wildcard because conflicting patterns were refused earlier. Here's why the first route found is the most specific one, which is worth understanding:

> Score each segment of a pattern: literal = 3, `{param}` = 2, `{rest...}` = 1. Pattern A is *more specific* than B if A's score is ≥ B's at every position and > at one or more. Any two routes that could match the same request must be comparable this way, or registration would have rejected them as a conflict. So among all routes matching a request, one of them is ≥ all the others in every position. The search tries literal, then param, then rest at each step, so it visits patterns in descending order of score from left to right ("lexicographic" order). The pattern that's ≥ everything at every position comes first in that order. **So the first match found is the most specific.** It's the same argument the ServeMux source refers to. Methods fit into this too: they're checked at the leaf, and conflict detection compares method and path together, so two routes that both match a request still can't tie.

### 5.5 Parameter extraction

- During matching, values go into a small array owned by the match call (`[8]string`, heap only past 8 parameters), recorded by position.
- On success, the router pairs each value with the route's `paramNames` and calls `r.SetPathValue(name, value)`. It also sets `r.Pattern = route.pattern`, which Milestone 3's request logging will use, because it's low-cardinality and has no IDs in it.
- `{rest...}` receives the remaining **decoded** segments joined with `/`, keeping a trailing slash (§5.4.1). Every `/` in that value is a real separator from the URL, never a decoded `%2F`, because phase 1 rejects those. That's a property of the URL only. It is not a promise that the value is safe to use as a file path (§6.8).
- A trade-off we measured: `SetPathValue` costs **2 allocations, 336 bytes** per request with parameters (go1.27.4). `ServeMux` avoids it by writing unexported request fields an outside router can't reach. The alternatives cost more: putting parameters in the request context means `r.WithContext`, which copies the whole request. We accept it, because **the point of parameters is that handlers can read them with the standard `r.PathValue`**. §8 sets the allocation budget around this instead of pretending it away.
- Values are pushed when a branch is entered and **popped when it's abandoned** (the pseudocode's `m.pop()`), so a value from a failed branch can never end up in the final match. This gets its own tests (§11.2).

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
| `{name...}` | everything after the slash that precedes it: one or more remaining segments, where the last may be the empty trailing one | remaining segments joined by `/`, decoded; a trailing slash is kept | must be last; `/static/{p...}` matches `/static/` (`p=""`) and `/static/a/` (`p="a/"`), never `/static`. Full table in §5.4.1 |
| `{$}` | only an empty final segment (a trailing slash) | none | `"/{$}"` = the root page only |

### 6.4 Methods

- A route may name one method or none (none = any method).
- `HEAD` uses the `GET` route when there's no explicit `HEAD` route; `net/http` drops the body for us.
- If the path matches routes but none for this method: **405 Method Not Allowed** with `Allow:` listing the methods that would have worked, sorted and with no duplicates, plus `HEAD` if `GET` is there. (Quinze, task 2.)
- **`OPTIONS` is an ordinary method** (Q5). A registered `OPTIONS /x` route answers it, and so does a method-less route on that path. Otherwise an `OPTIONS` request gets the normal 404 or 405 (with `Allow`), and nothing is synthesised. CORS preflight belongs to Milestone 3's CORS middleware.
- **A method-less route on a path means that path never answers 405**, because it accepts every method.
- **405 only describes the exact path.** If `POST /tree` arrives and only `GET /tree/` exists, the answer is **404**: `/tree` matches nothing, and a slash-variant is only ever considered for a GET/HEAD redirect (§6.5). That differs from ServeMux, which answers 405 there because its `/tree/` pattern is a subtree.
- Methods are case-sensitive. A request with `get` gets 405, the same as ServeMux.
- **Order of checks for one request:** phase 1 validation (400/414) → cleaning redirect or 400 (§6.6) → match → trailing-slash redirect for GET/HEAD (§6.5) → 405 → 404. Validation always comes first, so a malformed path never leaks routing information through 405 or `Allow`.

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
| Decoded segment containing `/` (`%2F`) | 400 | §3.3. No opt-out in Milestone 2 (Q4); if a real need appears, it gets its own design |
| Decoded segment `.` or `..` (`%2e`, `%2e%2e`) | 400 | the traversal bypass from §3.3 |
| `%00` or control characters after decoding | 400 | NUL truncation in C libraries and log injection (`\r\n`) |
| Invalid UTF-8 after decoding (including overlong forms like `%C0%AF`) | 400 | no two byte sequences that "look the same"; checked with `utf8.ValidString` (Q7) |
| Path longer than `MaxPathLength` (default 8 KiB) | 414 | bounds the work one request can cause |
| More than `MaxSegments` (default 128) | 414 | same; also bounds how deep the matching recursion goes |

### 6.8 What the router does *not* protect: filesystem access

URL validation and filesystem safety are **separate concerns**. The router's checks (§6.7) are about URLs: they make sure a path value means what the cleaned URL meant. They are not a filesystem security boundary, and no handler may treat them as one. Here's why they can't be:

- **Path values aren't the only input.** A file name can arrive in a query string (`?file=../../etc/passwd`), a form field, a JSON body, a header, or a database row. None of those pass through the router.
- **`{rest...}` values contain `/` by design**, and `filepath.Join` happily climbs out of a directory with a `..` built from pieces the router never saw as one.
- **The filesystem has its own rules the router can't know:** symlinks that point outside the folder, `\` as a separator on Windows, case-insensitive file names on macOS and Windows (so `/Secret.txt` = `/secret.txt`), Windows device names (`CON`, `NUL`), and NTFS alternate streams (`file.txt::$DATA`).
- **Routes change.** A future route, a proxy that decodes differently, or a handler mounted elsewhere would silently remove a protection that lived only in the router.

**So file access gets its own design, in Milestone 4 (static files and templates).** It will:

- open files only through `os.Root` (Go 1.24+), which refuses to leave its directory, symlinks and `..` included, at the operating-system level
- never build a file path with `filepath.Join(root, userInput)`
- have its own threat model and its own tests (symlink escapes, `..` from query strings, Windows-style paths, case variants), written as if the router didn't exist

The router's §6.7 checks stay as **defence in depth**: one layer, never the only one.

## 7. Concurrency safety

**Model: build, then freeze.** A router has two phases: *building* (routes are added; nothing is served yet) and *frozen* (the route table is fixed; requests are served). The switch between them is **explicit**:

```go
func (rt *Router) Freeze()        // idempotent: calling it again does nothing
func (rt *Router) Frozen() bool
```

**Who freezes, and when:**

| Usage | Who calls `Freeze` | When |
|---|---|---|
| `omolar.App` (the normal case) | the App | in `Run`/`Serve`, **after** checking `app.Err()` and **before** creating the server. So the table is final before the first connection is even accepted |
| A standalone `router.Router` (tests, other apps, `httptest`) | the developer, ideally | when setup is done |
| A standalone router whose owner forgot | **the router itself** | at the start of its first `ServeHTTP`. It's a safety net, so a standalone router is never served while still changing |

**Rules:**

1. `Handle` takes a `sync.Mutex` and checks `frozen` **while holding it**. Building is safe even from several goroutines. It's the slow path, run only at startup.
2. `Freeze` takes the same mutex, does the one-time finalising work, then sets `frozen` (an `atomic.Bool`). The finalising work: build the sorted `Routes()` snapshot, and give every slice its final, exact capacity.
3. `Handle` after the freeze changes nothing and returns `ErrFrozen`. In `omolar.App` that can only happen if code registers a route after `Run`, because before `Run` nothing is frozen. The App logs it at error level and records it in `app.Err()`; it doesn't panic in a running server.
4. `ServeHTTP` does `if !rt.frozen.Load() { rt.Freeze() }` and then reads the tree. After the freeze the tree is **never written again**, so any number of goroutines read it at once, and the hot path costs one atomic load and no lock.

**Why freezing takes the mutex.** Without it there's a race. A `Handle` call checks `frozen == false` and starts inserting nodes; at that moment a request freezes the router and starts reading the half-written tree. Because `Freeze` takes the registration mutex, it waits for any `Handle` already in progress to finish. Any `Handle` that starts later sees `frozen == true` under the lock and touches nothing.

**Why it's safe in Go's memory model.** A mutex unlock "happens before" the next lock of that mutex, and an atomic store happens before any load that observes it. So: every tree write (under the lock) → `Freeze` (takes the lock, finalises, stores `true`) → every request that loads `frozen == true` and reads the tree. Every write is guaranteed visible to every reader. With `omolar.App` there's a second guarantee on top: `Freeze` runs before `server.Serve` starts any goroutine, and the `go` statement itself creates a happens-before edge. The race detector (`-race`) checks all of this (§11.6).

**Why explicit beats the first-request freeze alone.** In revision 1 the first request froze the router. That works, but it moves a one-time setup step into the first user's request: finalising runs on the hot path, and a late `Handle` "works" until the first request and fails after it, which depends on timing. An explicit `Freeze` at startup makes the moment deterministic, keeps finalising out of request handling, and gives the App one obvious place to say "the route table is final". The first-request freeze stays only as a safety net for standalone use.

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
| `r.SetPathValue` | **yes**, routes with parameters only: 2 allocations, 336 bytes in go1.27.4 (§5.5) |
| 405 `Allow` header | yes, but it's an error path |

**Principle: correctness first; allocations are a budget, not a goal in themselves.** Revision 1 set "≤ 2 allocations" for parameter routes. That number came from today's `SetPathValue` implementation, which is a standard-library detail that can change in any Go release. A target tied to it would either break for reasons that have nothing to do with our code, or tempt us to dodge `SetPathValue` (and break `r.PathValue` for handlers) to hit a number. Neither is acceptable. So the budget is **relative**:

| Situation | Budget | Why it's not arbitrary |
|---|---|---|
| Static route (no parameters) | **0 allocations** | nothing in the design needs memory: segments are substrings, matching only reads |
| Route with parameters | **exactly what `r.SetPathValue` costs for that many values, plus 0** | the router adds nothing of its own; all the memory goes to giving handlers the standard `r.PathValue` |
| Segment containing `%` | **+ whatever `url.PathUnescape` needs** | decoding needs a new string, since a Go string can't be changed in place |
| 404 / 405 / 400 / redirects | **not budgeted** | error and redirect paths favour clear code; measured and reported only |

**How the relative budget is tested.** A test measures, with `testing.AllocsPerRun`, (a) the router serving a parameter route and (b) a reference function that does nothing but call `r.SetPathValue` the same number of times on an identical request. It asserts **(a) == (b)**. If Go makes `SetPathValue` cheaper, both drop and the test still passes. If our code adds an allocation, it fails, and points straight at us. The static-route test asserts 0 directly.

**What is never traded for speed:**

- **No reuse of parameter buffers across requests** (no `sync.Pool` for values handed to handlers). A pooled buffer reused too early could show one user's path values to another request. That's a data leak.
- **No `unsafe` string tricks** to skip copying in `PathUnescape`.
- **No skipping validation** (§6.7) on any path, for any reason.

**Baseline**, ServeMux on this 2-core sandbox (go1.27.4, 100 routes), for context, not as a target:

| Route | Time | Memory | Allocations |
|---|---|---|---|
| static | 199 ns/op | 0 B | 0 |
| 1 parameter | 248 ns/op | 16 B | 1 |
| `r.SetPathValue` alone (1 or 3 values) | 170 ns/op | 336 B | 2 |

ServeMux can beat our parameter-route allocations because it writes unexported request fields that only the standard library can reach. Matching that isn't possible without breaking `r.PathValue`, and isn't a goal. Speed numbers (ns/op) are **reported in the PR, not pass/fail**. Allocation budgets are pass/fail.

**"On the stack" isn't something Go promises.** The compiler's *escape analysis* decides whether a variable can live on the stack, which is free, or must go to the heap, where the garbage collector manages it. If a pointer to the `[32]string` array escaped (say, stored in a struct that outlives the call), the array would silently move to the heap. We check with `go build -gcflags=-m ./router` and keep the output in the PR. The allocation tests above are the real proof.

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

// Handle registers h for pattern, e.g. "GET /users/{id}". This is the
// checked registration API: the error comes straight back to the caller.
// It returns a *PatternError for a malformed pattern, a *ConflictError if
// the pattern is ambiguous with an existing one, or ErrFrozen after Freeze.
// A failed Handle leaves the router exactly as it was.
func (rt *Router) Handle(pattern string, h http.Handler) error
func (rt *Router) HandleFunc(pattern string, fn func(http.ResponseWriter, *http.Request)) error

// Freeze ends the building phase: the route table becomes read-only and
// later Handle calls return ErrFrozen. Calling it again does nothing.
// omolar.App calls it at startup; a router that was never frozen freezes
// itself on its first request (§7).
func (rt *Router) Freeze()
func (rt *Router) Frozen() bool

// ServeHTTP routes a request (Router is an http.Handler).
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
// No option to allow %2F (Q4: deferred). Encoded slashes are always rejected.

// Errors
type PatternError struct { Pattern string; Offset int; Reason string }
type ConflictError struct { New, Existing string; Example string }
var ErrFrozen = errors.New("router: routes cannot be added after Freeze")
```

**A deliberate omission:** no `Get(path, h)` / `Post(path, h)` helper methods yet. `"GET /path"` already says it, and one way to register routes is easier to audit. They're easy to add later if wanted.

### 10.2 What changes in `omolar.App`

| | Milestone 1 | Milestone 2 |
|---|---|---|
| Router | `*http.ServeMux` | `*router.Router` |
| `app.Handle`, `app.HandleFunc` | same signatures | same signatures; a bad or conflicting pattern is **recorded** (§10.3) |
| `app.Err()` | n/a | **new**: every registration error so far, or nil |
| `app.Run`, `app.Serve` | start serving | 1. return `app.Err()` if non-nil, without opening a port; 2. call `router.Freeze()`; 3. log the route table; 4. start serving |
| `/health` | registered as `"/health"` (any method; the handler itself returns 405) | `"GET /health"`: the router handles HEAD and 405, and the handler's own check stays as a second line of defence |
| Startup log | "server started" | also logs the route table (`Routes()`), one line per route, at debug level |
| `r.PathValue`, `r.Pattern` | set by ServeMux | set by the Omolar router: handlers don't change |

All Milestone 1 patterns (`"GET /{$}"`, `"GET /hello/{name}"`, `"GET /teapot"`) are valid unchanged. The existing tests become regression tests for the switch.

**Freezing and re-running.** An App can be stopped and run again (a Milestone 1 test checks this). `Freeze` is idempotent, so a second `Run` just finds the router already frozen and serves the same table. Calling `app.Handle` after the first `Run` is recorded as `ErrFrozen` in `app.Err()` and logged at error level. The running server is unaffected, and the next `Run` refuses to start until the code is fixed.

### 10.3 Registration errors in the App (Q1: decided)

**Decision:** `app.Handle` and `app.HandleFunc` **record** errors; `app.Err()` exposes them; `Run`/`Serve` refuse to start and report every one of them. The **checked API that returns errors directly is `router.Router.Handle`**. As requested, here's the evaluation of whether the App should *also* get a checked method:

| Option | For | Against |
|---|---|---|
| **A. Record + `app.Err()` + refuse to start** (chosen) | app code stays tidy; every problem reported at once (the same Notification pattern as `config.Validate`); impossible to start with a broken table | the error surfaces at `Run` or `Err()`, not on the line that caused it |
| B. A second, checked App method (`app.TryHandle(p, h) error`) | the error is right at the call site | two ways to register routes, which is harder to audit (the same reason `Get`/`Post` helpers were deferred, Q8); its error is easy to ignore with `_ =`; adds nothing A can't detect |
| C. Expose `app.Router()` for direct checked calls | full router API available | routes registered there would bypass the App's error list, and code could call `Freeze` or `ServeHTTP` out of order. Splits ownership of one table between two objects |
| D. Change `app.Handle` to return `error` | the most explicit | breaks every Milestone 1 call site; Go linters flag every unchecked call, so real apps end up wrapping it in a helper that panics, which is just ServeMux's behaviour again |

**Why A loses nothing important:**

- **Every route is registered before `Run`**, and `Run` won't start with errors, so a checked method could only report a problem *earlier*, never catch one A would miss.
- **The call site isn't lost.** Each recorded error includes the file and line of the `app.Handle` call that caused it (captured with `runtime.Caller`, the way ServeMux's panic message reads "registered at main.go:81"), so it's as easy to find as a returned error.
- **The errors stay typed.** `app.Err()` is an `errors.Join` of `*router.PatternError`, `*router.ConflictError` and `ErrFrozen` values, so tests can still check exact failures with `errors.As` and `errors.Is`.
- **Standalone and unit-test use already has the checked API**: `router.Router.Handle` returns the error directly.

**Revisit B or C only if a concrete need appears**, such as a plugin system that registers routes it didn't write and has to react to each failure on its own. That would get its own small design.

## 11. Test plan

All tests are written first, per CLAUDE.md. Each group becomes a red commit, then a green one. Every matrix row below is one table-driven test case.

### 11.0 Test levels

Behaviour is checked at the level where it can actually go wrong:

| Level | How | Good for | Blind spots |
|---|---|---|---|
| **L1 Tree** | `routetree` with `V = string`, no HTTP | matching, precedence, backtracking, conflicts, param positions | anything HTTP |
| **L2 Direct handler** | `router.ServeHTTP(httptest.NewRecorder(), req)` | status codes, headers, which handler ran, `r.PathValue`, `r.Pattern`; fast and deterministic | the recorder **keeps** HEAD bodies (a real server drops them); `httptest.NewRequest` parses the target, so some hostile paths must be built by setting `req.URL` by hand |
| **L3 Real server** | `httptest.NewServer(router)`, plus `omolar.App` on a listener the test holds (Milestone 1 helpers), using a real `http.Client` with redirects turned off | what actually goes over the wire: HEAD has no body bytes, `Allow` and `Location` survive, redirects work with a real client | paths a normal client won't send |
| **L4 Raw TCP** | `net.Dial`, then write the request line by hand (`GET /a/../b HTTP/1.1`) | hostile request lines that Go's client would normalise or refuse (`//evil.com`, `/%zz`, raw `..`, absolute-form `GET http://x/y`): shows what `net/http` hands the router, and how we answer | none for routing; slow, so used sparingly |

The column "Levels" in the tables says where each row runs.

### 11.1 Pattern parsing (`routetree`, L1)

A table of valid patterns and what they parse into (method, segments, kinds, names). A table of invalid ones, each with the expected `Offset` and `Reason`, covering every rule in §5.2. `splitPath` has its own table, written into the task card (`docs/tasks/01-split-path.md`).

### 11.2 Precedence and wildcard backtracking (L1, key rows repeated at L2)

| # | Routes | Request | Expected route | Params | What it proves |
|---|---|---|---|---|---|
| P1 | `/users/me`, `/users/{id}` | `/users/me` | `/users/me` | | literal beats param |
| P2 | same | `/users/42` | `/users/{id}` | id=42 | |
| P3 | `/users/{id}` | `/users/` | no match | | `{id}` never matches the empty trailing segment |
| P4 | `/a/b/z`, `/a/{x}/c` | `/a/b/c` | `/a/{x}/c` | x=b | one-level backtrack |
| P5 | `/a/b/c/z`, `/a/b/{y}/w`, `/a/{x}/c/d` | `/a/b/c/d` | `/a/{x}/c/d` | x=b, **no `y`** | two-level backtrack; a value from an abandoned branch doesn't leak |
| P6 | `/a/{x}/c`, `/a/{y...}` | `/a/b/d` | `/a/{y...}` | y=b/d, **no `x`** | param → rest backtrack, popped value |
| P7 | `/files/{name}/meta`, `/files/{p...}` | `/files/a/b` | `/files/{p...}` | p=a/b | |
| P8 | `/static/app.js`, `/static/{p...}` | `/static/app.css` | `/static/{p...}` | p=app.css | literal → rest |
| P9 | `/static/{name}`, `/static/{p...}` | `/static/app.js` / `/static/css/app.css` | `{name}` / `{p...}` | | param beats rest when it fits |
| P10 | every row of §5.4.1 | | | | terminal rest: empty remainder, trailing slash, root catch-all |
| P11 | `/{a}/x/{b}` | `/1/x/2` | `/{a}/x/{b}` | a=1, b=2 | params by position |
| P12 | `/users/{id}`, `/users/{uid}/posts` | `/users/7` / `/users/7/posts` | each its own | id=7 / uid=7 | two routes share one param node but keep their own names |
| P13 | `/{$}`, `/{p...}` | `/` / `/x` | `/{$}` / `/{p...}` | p=x | `{$}` beats rest at the root |
| P14 | a 30-route set | every route's own example path | itself | | **order independence**: inserted in 50 random orders (fixed seed); identical results every time |

### 11.3 Method precedence (L1 `pick` rules, full matrix at L2, starred rows also at L3)

| # | Routes | Request | Expected |
|---|---|---|---|
| M1 | `GET /x`, `/x` | `GET /x` | `GET /x` |
| M2 | same | `PUT /x` | `/x` (method-less) |
| M3* | same | `HEAD /x` | `GET /x` (HEAD → GET beats method-less) |
| M4* | `HEAD /x`, `GET /x` | `HEAD /x` / `GET /x` | `HEAD /x` / `GET /x` (an explicit HEAD wins) |
| M5 | `/x` | `OPTIONS /x`, `HEAD /x`, `PURGE /x` | `/x` for all of them |
| M6* | `GET /users/{id}`, `POST /users/new` | `GET /users/new` | `GET /users/{id}`, id=new (a method mismatch backtracks) |
| M7 | same | `POST /users/new` | `POST /users/new` |
| M8 | same | `HEAD /users/new` | `GET /users/{id}`, id=new |
| M9* | same | `DELETE /users/new` | **405, `Allow: GET, HEAD, POST`** (methods collected from both leaves) |
| M10 | `GET /files/{p...}`, `PUT /files/{name}` | `GET /files/a` | `GET /files/{p...}`, p=a |
| M11 | same | `PUT /files/a` | `PUT /files/{name}` |
| M12 | same | `PUT /files/a/b` | 405, `Allow: GET, HEAD` |
| M13 | same | `DELETE /files/a` | 405, `Allow: GET, HEAD, PUT` |
| M14 | `OPTIONS /x`, `GET /x` | `OPTIONS /x` / `POST /x` | `OPTIONS /x` / 405 `Allow: GET, HEAD, OPTIONS` |
| M15 | `PURGE /cache` | `PURGE /cache` / `GET /cache` | match / 405 `Allow: PURGE` |
| M16 | `GET /x` | `get /x` | 405, `Allow: GET, HEAD` (methods are case-sensitive) |

### 11.4 404, 405 and the `Allow` header (L2 + L3)

**`Allow` generation** (these rows are the tests for Quinze's task 2):

| # | Methods matching the path | `Allow` |
|---|---|---|
| A1 | GET | `GET, HEAD` |
| A2 | GET, HEAD (explicit) | `GET, HEAD` (no duplicate) |
| A3 | HEAD | `HEAD` (HEAD doesn't imply GET) |
| A4 | POST, PUT, DELETE (registered in that order) | `DELETE, POST, PUT` (sorted) |
| A5 | GET, PURGE | `GET, HEAD, PURGE` (custom methods included) |
| A6 | GET, OPTIONS | `GET, HEAD, OPTIONS` (only because OPTIONS is registered) |
| A7 | GET on one leaf, POST on another (M9) | `GET, HEAD, POST` (union across leaves) |
| A8 | a method-less route on the path | never 405: it matches every method |

**404 versus 405 versus 400:**

| # | Situation | Expected |
|---|---|---|
| N1 | no route matches the path for any method | 404, no `Allow` |
| N2 | only `GET /tree/`; request `POST /tree` | 404 (405 only describes the exact path, §6.4) |
| N3 | only `GET /tree/`; request `GET /tree` | 307 → `/tree/` (§6.5) |
| N4 | invalid path (`/a%2Fb`) on a path that would 405 | **400**, and no `Allow`: validation runs first and leaks nothing about routes |
| N5 | `WithNotFound(h)` | `h` runs for 404 |
| N6 | `WithMethodNotAllowed(h)` | `h` runs for 405, with `Allow` **already set** when it starts |
| N7* | 405 over a real connection | status 405, `Allow` present on the wire, body `Method Not Allowed` |

### 11.5 HEAD behaviour (L2 and L3: the levels disagree here, so both are needed)

| # | Routes | Check | Level |
|---|---|---|---|
| H1 | `GET /x` writing a body | the GET handler runs, and sees `r.Method == "HEAD"` | L2 |
| H2 | same | **no body bytes on the wire**, status and headers match a GET (`Content-Type`, and `Content-Length` if net/http sends it for GET) | L3 |
| H3 | same | the L2 recorder *does* contain the body: documented in the test, so nobody "fixes" the router to strip it | L2 |
| H4 | `HEAD /x` + `GET /x` | the HEAD handler runs, not GET | L2, L3 |
| H5 | `POST /x` only | `HEAD /x` → 405, `Allow: POST` | L2 |
| H6 | `GET /static/{p...}` | `HEAD /static/a/` → the rest route, p=`a/` | L2 |
| H7 | `/health` in `omolar.App` | `HEAD /health` → 200, no body; `POST /health` → 405 `Allow: GET, HEAD` | L3 (App) |

### 11.6 Trailing-slash redirects (L2 + L3; **Quinze writes these tests**, task 3)

The rules from §6.5. The task card will give these as requirements, not as finished tests: 307 for GET/HEAD only; the query string kept; the redirect target is the slash-variant that actually matches **for the same method**; no redirect for POST/PUT/etc.; none when the exact path already matches; none when switched off; never a loop (the target must match without redirecting again); at L3, a real client with redirects disabled sees the 307 and the `Location`.

### 11.7 Path cleaning (L2, L3, L4)

`//a`, `/a/./b`, `/a/../b`, `/a/b/..`: 307 to the clean path for GET/HEAD, keeping the query; 400 for POST/PUT/PATCH/DELETE. At L4: send the raw request lines, to confirm the router (not the client) does the cleaning.

### 11.8 Security edge cases (one test each; L2, with ★ rows also at L4)

| Case | Expected |
|---|---|
| ★ `/static/a%2F..%2F..%2Fetc%2Fpasswd` (the §3.3 bypass) | 400, handler never called |
| `/users/a%2Fb` | 400 (always: no opt-out, Q4) |
| `/users/%2e%2e`, `/users/%2E`, `/users/.%2e` | 400 |
| `/a%00b`, `/a%0d%0aSet-Cookie:x` | 400 |
| `/caf%C3%A9` (valid UTF-8) | 200 when `/café` is registered |
| `/%C0%AF` (overlong UTF-8 for `/`), `/%FF` | 400 (Q7) |
| ★ a path that cleans to `//evil.com` | `Location` never starts with `//` |
| ★ `/\evil.com` | `Location` uses `%5C` |
| 8 KiB + 1 path / 129 segments | 414 |
| `/Admin` when only `/admin` is registered | 404, never matched case-insensitively |
| ★ `/%zz` (invalid escape), absolute-form `GET http://other/x` | whatever `net/http` does before the router (recorded by the test), and never a 200 |
| a `{rest...}` value with `..` built from separate segments (`/files/a/..` with GET) | cleaned or rejected before matching; the handler never sees `..` |

(Filesystem traversal itself is tested in Milestone 4, against `os.Root`, independently of these, §6.8.)

### 11.9 Freezing and concurrency (L2 and App level, all with `-race`)

| # | Check |
|---|---|
| F1 | `Freeze()` twice: no error, no change; `Frozen()` reports true |
| F2 | `Handle` after `Freeze` → `ErrFrozen`, and `Routes()` is unchanged |
| F3 | standalone router, never frozen: the first `ServeHTTP` freezes it; a later `Handle` → `ErrFrozen` |
| F4 | `Freeze` racing several `Handle` calls: every `Handle` either succeeds (and its route works) or returns `ErrFrozen`; never a half-added route |
| F5 | 100 goroutines × 1,000 mixed requests against a frozen router: no race reports, every answer correct |
| F6 | `Handle` from several goroutines before freezing: all routes present |
| F7 | App: a bad pattern → `app.Err()` non-nil with the caller's file:line; `Run` returns it **without opening a port** |
| F8 | App: `Run` freezes the router; `app.Handle` afterwards → recorded `ErrFrozen` and an error log line; the running server keeps serving the old table |
| F9 | App: stop and `Run` again (Milestone 1 test) still works with a frozen router |
| F10 | App: `errors.As(app.Err(), &conflictErr)` finds the `*router.ConflictError` |

### 11.10 Fuzzing (Go native fuzzing, `go test -fuzz`)

- **`FuzzSplitPath` / `FuzzParsePattern`:** random strings never panic. Anything that parses can be re-printed and parsed again to the same result ("round-trips").
- **`FuzzMatch`:** random paths against a fixed route set never panic. Any 200 came from a registered pattern. Filling the returned params into the pattern rebuilds a path that cleans to the same thing.
- **`FuzzAgainstServeMux` (differential):** the same route set, avoiding the documented differences (§3.4), in both ServeMux and Omolar; fuzzed GET paths; both must pick the same pattern, or both 404/405. **The standard library is the reference we're checked against.** A disagreement outside the documented differences means ours is wrong.
- Seed corpora include every row of §11.8. Each fuzz target also runs over its corpus as an ordinary test in `make test` and CI, and runs for real on demand (`make fuzz`).

### 11.11 Allocation budget tests and benchmarks

**Pass/fail allocation tests** (`testing.AllocsPerRun`, §8): a static route = 0; a parameter route = the reference `SetPathValue`-only function, measured in the same test; the same with a `%`-encoded segment, plus the `PathUnescape` reference.

**Benchmarks** (`b.Loop`, `b.ReportAllocs`, each next to ServeMux on the same machine; reported, not pass/fail):

| Benchmark | Shape |
|---|---|
| `Static` | `/api/v1/status` among 100 routes |
| `Param1` / `Param3` | `/users/{id}`, `/orgs/{org}/repos/{repo}/issues/{n}` |
| `Rest` | `/static/{p...}` with 5 trailing segments |
| `Deep` | 10 literal segments |
| `Backtrack` | P5 |
| `NotFound` / `MethodNotAllowed` | error paths |
| `GitHubAPI` | ~250 real-world-shaped routes (GitHub's REST layout), a mix of lookups |
| `Parallel` | `b.RunParallel` over `GitHubAPI` (where avoiding a lock should show) |
| `Register1000` | building the table, checking §9's startup cost estimate |
| `StaticChildren` (internal) | sorted slice vs map vs hybrid at 4, 16 and 64 children: decides §5.3 |

### 11.12 Milestone 1 regression

Every Milestone 1 test keeps passing unchanged, apart from the deliberate move of `/health` to `"GET /health"`, which H7 covers. The Docker CI job (image build, HEALTHCHECK, graceful stop) must stay green.

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

Commits follow red → green from CLAUDE.md. **Q** = Quinze writes it. For Q's code tasks, Claude writes the failing tests and a stub, then reviews; Claude never writes Q's implementation.

| # | Step | Who | Task card |
|---|---|---|---|
| 1 | `routetree`: `splitPath` tests + stub, then **`splitPath` (task 1)** | tests: Claude · code: **Q** | [`docs/tasks/01-split-path.md`](../tasks/01-split-path.md) |
| 2 | `routetree`: the rest of the pattern parser (segment kinds, names, methods) | Claude | |
| 3 | `routetree`: node, insert, match, `pick`, precedence, backtracking, terminal rest (§5.4, §5.4.1) | Claude | |
| 4 | `routetree`: conflict detection with example paths | Claude | |
| 5 | `routetree`: **`Routes()` depth-first walk (task 4)** | tests: Claude · code: **Q** | `04-routes-traversal.md` (written when step 3 lands) |
| 6 | `router`: validation phase, cleaning, limits, security cases (§6.6–6.8) | Claude | |
| 7 | `router`: ServeHTTP, 404, `Freeze`/`Frozen`, auto-freeze, options | Claude | |
| 8 | `router`: **`Allow` header for 405 (task 2)** | tests: Claude · code: **Q** | `02-allow-header.md` |
| 9 | `router`: trailing-slash redirect. **Tests (task 3)** | rules: Claude · tests: **Q** · code: Claude | `03-trailing-slash-tests.md` |
| 10 | `omolar.App`: router, `app.Err()` with caller locations, freeze in `Run`/`Serve`, route log | Claude | |
| 11 | L3/L4 tests, fuzz targets including the ServeMux differential | Claude | |
| 12 | Allocation-budget tests, benchmarks, escape-analysis check; results in the PR | Claude | |
| 13 | `docs/learning/02-router.md` | Claude | |

Each Q task's card gives the goal, the tests to make pass, the Go and C/C++ concepts involved, worked examples, and hints that give away progressively more. Steps that depend on a Q task wait for it (5 needs 3; 9's code waits for Q's tests); others can carry on meanwhile.

## 14. Review decisions

Decided in the independent review of revision 1.

| # | Question | Decision | Where it's reflected |
|---|---|---|---|
| Q1 | Registration errors in `App` | **Collect, `app.Err()`, refuse to start.** The checked API is `router.Router.Handle`; a second checked App method was evaluated and not added (§10.3) | §10.2, §10.3, F7–F10 |
| Q2 | Trailing-slash patterns | **Exact matching; subtrees must be explicit `{rest...}`** | §6.5, §5.4.1 |
| Q3 | Non-canonical path on POST/PUT/... | **400** | §6.6, §11.7 |
| Q4 | `%2F` inside a `{param}` | **Rejected, always.** The opt-in option is removed from Milestone 2 | §6.7, §10.1, §11.8 |
| Q5 | Automatic `OPTIONS` | **None.** Only explicitly registered routes answer it | §6.4, M14, A6 |
| Q6 | Radix compression | **Deferred until benchmarks justify it** | §4, §11.11 |
| Q7 | Invalid UTF-8 in a decoded segment | **400** | §6.7, §11.8 |
| Q8 | `Get`/`Post` helpers | **Deferred** | §10.1 |

## 15. Revision history

**Revision 2** (after independent review):

| Change | Section |
|---|---|
| Review decisions Q1–Q8 applied; the encoded-slash option removed | §1, §3.4, §6.7, §10.1, §14 |
| **Correction 1:** the matching algorithm handles terminal `{rest...}` explicitly: only tried while a segment remains, value keeps a trailing slash, empty remainder allowed, its own method table. Fixes a revision 1 bug where the node stored a single rest *route*, so `GET` and `PUT` on the same `{rest...}` couldn't coexist. Values pushed and popped during backtracking | §5.3, §5.4, §5.4.1, §5.5, §6.3 |
| **Correction 2:** explicit `Router.Freeze()`/`Frozen()`, called by `omolar.App` at startup; first-request auto-freeze kept only as a safety net for standalone routers; late `app.Handle` behaviour defined | §7, §10.1, §10.2, F1–F10 |
| **Correction 3:** URL validation is defence in depth only and not a filesystem boundary; file access becomes an independent Milestone 4 concern built on `os.Root` | §2, §3.3, §5.5, §6.8 |
| **Correction 4:** test levels L1–L4 (tree, direct handler, real server, raw TCP); matrices for precedence and backtracking (P1–P14), method precedence (M1–M16), `Allow` (A1–A8), 404/405/400 (N1–N7), HEAD (H1–H7), freezing (F1–F10) | §11 |
| Performance targets: fixed allocation counts replaced by a relative budget measured against `SetPathValue` itself; things never traded for speed listed | §8, §11.11 |
| §6.4 now states the order of checks, and that 405 only describes the exact path | §6.4 |
| Q1 evaluation: four registration API options compared; caller file:line added to recorded errors | §10.3 |

**Revision 1:** initial proposal.
