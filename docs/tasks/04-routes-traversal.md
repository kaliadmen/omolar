# Task 4: `Routes()` traversal

| | |
|---|---|
| **Owner** | Quinze writes the implementation. Claude wrote the failing tests and the stub, and reviews |
| **Milestone / step** | Milestone 2, step 5 ([design §13](../design/02-router.md#13-work-plan-and-who-builds-what)) |
| **File** | `internal/routetree/walk.go` |
| **Difficulty** | ★★☆: recursion over a real tree, plus an ordering rule to get exactly right |
| **Time** | 45–120 minutes |

## Why this matters

When an Omolar app starts, it logs its route table, and later the CLI will print it. To list the routes you have to **visit every node of the trie**. That's depth-first traversal, one of the most common interview questions there is, and here you get to do it on a data structure you've actually read.

It's also how you prove to yourself that you understand the trie: you can't walk it without knowing where routes live.

## What's already there

Read `internal/routetree/tree.go` first, especially the `node`, `table` and `route` types and the `match` method. Then:

- `walk.go`: the `RouteInfo` type (finished) and a stub `Routes()` returning `nil`.
- `walk_test.go`: the tests below. They fail until you're done.

**You write the body of `Routes()`, plus any small helper functions or closures you want, in `walk.go` only.** Don't change the tests or `tree.go`. If you think a test is wrong, say so.

## Where routes live in the trie

Every route sits in a `table` (a set of routes for one position, by method). A node has up to two tables:

| Field | Holds routes that... | Example |
|---|---|---|
| `n.leaf` | **end** at this node | `GET /users` ends at the `users` node |
| `n.rest` | end in a `{name...}` **at** this node | `GET /static/{p...}` is in the `static` node's `rest` |

Inside a table:
- `byMethod`: routes with a method, in **insertion** order (not sorted)
- `any`: the one method-less route, or nil

A node's children:
- `labels[i]` / `statics[i]`: literal children, **already sorted** by label
- `param`: the `{name}` child, or nil

## Requirements

| # | Requirement |
|---|---|
| R1 | Return **every** route in the tree, each exactly once, as `RouteInfo{Method, Pattern}`. `Pattern` is `route.pat.raw`, exactly as registered (so `GET /{$}` stays `GET /{$}`). |
| R2 | Find them by **walking the nodes**, starting at `t.root`. Don't read `t.routes`. A test sets it to nil to check. |
| R3 | Visit in **depth-first pre-order**, at each node: (1) the node's `leaf` table, (2) its literal children in `labels` order, (3) its `param` child, (4) its `rest` table. That's the same "most specific first" order `match` uses, with shorter paths before longer ones. |
| R4 | Within one table: routes with a method, **sorted by method name**, then the method-less route last. |
| R5 | The result must not depend on insertion order. (R3 and R4 make that true; a test inserts in 20 random orders.) |
| R6 | **Read-only.** Never change the tree, not even to sort a slice in place. `Routes` may be called while requests are being served; a test runs it alongside `Match` under the race detector. |
| R7 | Return a new slice each call, so a caller changing it can't affect the next call. |
| R8 | An empty tree returns an empty (or nil) slice. Don't panic on nil `leaf`, `rest` or `param`. |

## Worked example

The test inserts these (scrambled):

```
GET /users/{id}    POST /users          /health            GET /static/{p...}
GET /users/me      DELETE /users/{id}   GET /              PUT /static/{p...}
GET /users/{id}/posts                   GET /static/app.js OPTIONS /health
GET /users
```

The trie they build:

```
(root)
├── ""        leaf: GET /
├── "health"  leaf: OPTIONS /health, /health (any)
├── "static"
│   ├── "app.js"  leaf: GET /static/app.js
│   └── rest:     GET /static/{p...}, PUT /static/{p...}
└── "users"   leaf: GET /users, POST /users
    ├── "me"      leaf: GET /users/me
    └── {param}   leaf: DELETE /users/{id}, GET /users/{id}
        └── "posts"   leaf: GET /users/{id}/posts
```

`Routes()` must return:

| # | Method | Pattern | Why it's here |
|---|---|---|---|
| 1 | GET | `GET /` | root's literal child `""` comes first (`""` sorts before everything) |
| 2 | OPTIONS | `OPTIONS /health` | `health` leaf: methods sorted... |
| 3 | | `/health` | ...then the method-less route last (R4) |
| 4 | GET | `GET /static/app.js` | `static` has no leaf; its literal child first (R3 step 2) |
| 5 | GET | `GET /static/{p...}` | then `static`'s rest table (step 4), sorted |
| 6 | PUT | `PUT /static/{p...}` | |
| 7 | GET | `GET /users` | `users` leaf, sorted: GET before POST |
| 8 | POST | `POST /users` | |
| 9 | GET | `GET /users/me` | literal children before `{param}` |
| 10 | DELETE | `DELETE /users/{id}` | the `{param}` node's leaf, sorted |
| 11 | GET | `GET /users/{id}` | |
| 12 | GET | `GET /users/{id}/posts` | then `{param}`'s children |

Trace it by hand against the diagram before coding. If you can produce this table yourself, the code is just writing it down.

## The tests you'll be making pass

| Test | Checks |
|---|---|
| `TestRoutes_Empty` | R8 |
| `TestRoutes_Order` | R3, R4 with the worked example |
| `TestRoutes_SameForAnyInsertOrder` | R5 (20 shuffles) |
| `TestRoutes_ReportsPatternsAsRegistered` | R1 (`{$}` kept as written) |
| `TestRoutes_WalksTheTree` | R2 (`t.routes` set to nil) |
| `TestRoutes_FindsEveryRoute` | R1 on the 28-route set from `tree_test.go` |
| `TestRoutes_ReturnsACopy` | R7 |
| `TestRoutes_ReadOnly` | R6, under `-race` |

```sh
make docker-shell
go test -race ./internal/routetree -run Routes -v
```

Then `make docker-test` (all green now) and `make docker-report`.

## Go concepts you'll use

- **Recursion.** A tree is defined in terms of smaller trees, so the natural traversal is a function that handles one node and calls itself for each child. Each call gets its own stack frame, and the call stack remembers where you were.
- **A recursive closure.** If you write the walker as a closure (so it can append to a result slice in the enclosing function), Go needs the variable declared **before** the function that uses it: `var walk func(n *node[V])` first, then `walk = func(n *node[V]) { ... walk(child) ... }`. A plain `walk := func...` can't refer to itself. A separate method like `func (n *node[V]) collect(out []RouteInfo) []RouteInfo` works too; both are fine.
- **Methods on a generic type.** Inside `func (t *Tree[V]) Routes()`, you're in a world where `V` is known, so you can write `*node[V]`, `*table[V]` and `*route[V]` freely.
- **Nil checks, with a choice to make.** `n.leaf`, `n.rest` and `n.param` are often nil. You can check before visiting, or give a helper a nil-safe receiver like `pick` has (`if tb == nil { return }`). Look at how `tree.go` does it.
- **Sorting without modifying.** `slices.Sort` and `slices.SortFunc` sort **in place**. Sorting `tb.byMethod` directly would change the tree and break R6. **Copy first** (`slices.Clone`) or collect the methods into your own slice, then sort that. `slices.SortFunc(s, func(a, b T) int { return strings.Compare(...) })` takes a comparison function that returns negative, zero or positive.
- **Growing a result slice.** Start with `var out []RouteInfo` and `append` to it, or create it with `make` if you know a size. (You can't use `len(t.routes)` for that here, because R2's test sets `t.routes` to nil. Why does that make no difference to correctness?)

## C and C++ comparison

| In this task | C / C++ | Difference worth noticing |
|---|---|---|
| Recursive DFS | the same recursive function | In C a deep recursion can overflow a fixed-size stack (often 1–8 MB) and crash. Goroutine stacks grow automatically. A route trie is shallow anyway (at most `MaxSegments` deep) |
| Recursive closure | `std::function<void(Node*)> walk = [&](Node* n){ ... walk(c); };` | Same trick in both languages: the variable must exist before the lambda that calls it. C++'s `[&]` capture by reference ≈ Go closures capturing variables |
| Reading `n.statics` | `std::vector<Node*>` | Same idea. Iterating with `for _, c := range n.statics` is like `for (Node* c : statics)` |
| "Don't modify the tree" | a `const` method: `std::vector<RouteInfo> routes() const;` | **Go has no `const` methods.** The compiler won't stop you sorting `byMethod` in place. The protection is convention plus the **race detector**, which is why R6 has a concurrent test. In C++, `const` would turn the mistake into a compile error |
| `slices.Clone` then sort | `auto copy = v; std::sort(copy.begin(), copy.end(), cmp);` | Same. And in both, a comparator must be consistent (a "strict weak ordering"), or the sort can misbehave |
| Returning `[]RouteInfo` | returning `std::vector<RouteInfo>` by value (moved) | Go returns a slice header (3 words) pointing at a new array. R7's test catches returning an internal slice the caller could change |

## Hints

Try for at least 20 minutes before opening one. You can also ask me for one in chat.

<details>
<summary>Hint 1: two jobs, two helpers</summary>

There are really two separate jobs: **"list the routes in one table, in R4 order"** and **"visit the nodes in R3 order"**. Write the table part as its own small function or closure first, and test it by thinking about the `/health` node. Then the node walk becomes four lines that call it.

</details>

<details>
<summary>Hint 2: the order of the four steps</summary>

The four steps at each node are: the leaf table, then each literal child (recursively), then the param child (recursively), then the rest table. Notice that the rest table comes *after* the param child's whole subtree. Why does `GET /static/app.js` (row 4) come before `GET /static/{p...}` (row 5) in the worked example? Because `app.js` is a literal child of `static`, visited in step 2, while the rest routes belong to `static`'s step 4.

For R4: copy `byMethod` into a new slice of `*route[V]`, sort the copy by `r.pat.method`, append those, then append `any` if it isn't nil.

</details>

<details>
<summary>Hint 3: the shape of it (no code)</summary>

1. In `Routes`, make an empty result slice.
2. Declare a table helper that does nothing for a nil table; otherwise it appends the sorted method routes and then `any`.
3. Declare a node walker (remember the `var walk func(...)` trick if it's a closure) that does the four steps from R3, calling itself for children.
4. Call the walker on `&t.root` (the root is a value inside `Tree`, so take its address) and return the result.
5. Run `TestRoutes_Order` first. If the order is wrong, print your result next to the worked-example table and find the first row that differs: that tells you which step is in the wrong place.

</details>

## Done when

- [ ] All `Routes` tests pass, including under `-race`
- [ ] `make docker-test` passes (the whole repo is green again)
- [ ] `gofmt` leaves the file unchanged and `go vet ./...` is clean
- [ ] You can trace the worked example by hand without looking at your code
- [ ] Committed under **your** name, `feat(routetree): implement Routes traversal (green)`, and pushed to `milestone-2/router`
- [ ] You've told me, and I'll review it

## Explain it back

1. What is depth-first pre-order traversal, and how is it different from breadth-first? Which order would breadth-first produce for the worked example's first three levels?
2. Why must `Routes` copy `byMethod` before sorting, when sorting in place would pass every test except one? What would actually go wrong in production?
3. Each node is visited exactly once. What's the time complexity of `Routes` in terms of nodes N and routes R? And what's the extra space used by the recursion?
4. Why can't the test simply compare `Routes()` with `t.routes`, and what does `TestRoutes_WalksTheTree` prove that the other tests don't?
5. In C++ you'd mark `routes()` as `const`. What does Go use instead to catch the same mistake, and what's its weakness compared with a compiler check?

## Stretch goals (optional)

- **An iterator version.** Go 1.23 added range-over-func iterators. Add `func (t *Tree[V]) All() iter.Seq[RouteInfo]` that yields routes one at a time in the same order, so callers can write `for r := range tree.All() { ... }` and stop early without building a slice. Then write `Routes` as `slices.Collect(t.All())`. Write a test for early stopping.
- **No recursion.** Write the walk with an explicit stack (a slice you push nodes onto and pop from). Getting the same order back is the tricky part: in what order must you push the children?
