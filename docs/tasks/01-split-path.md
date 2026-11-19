# Task 1: `splitPath`

| | |
|---|---|
| **Owner** | Quinze writes the implementation. Claude writes the failing tests and the stub, then reviews |
| **Milestone / step** | Milestone 2, step 1 ([design §13](../design/02-router.md#13-work-plan-and-who-builds-what)) |
| **File** | `internal/routetree/pattern.go` |
| **Difficulty** | ★☆☆: a warm-up, but every later router step depends on it being exactly right |
| **Time** | 30–90 minutes |

## Why this matters

Every route pattern (`"GET /users/{id}/posts"`) has to be broken into its path segments (`users`, `{id}`, `posts`) before it can go into the trie. `splitPath` does that first cut. The trie's rule for trailing slashes and the root path only works if this function follows the splitting rule from [design §5.2](../design/02-router.md#52-pattern-grammar) **exactly**. An off-by-one here becomes a routing bug later.

It runs once per route at startup, not per request, so it may allocate. But you'll learn the same string-slicing technique the per-request code uses in step 6.

## What you get, and what you write

Claude's red commit (`test(routetree): specify splitPath (red)`) will add:

- `internal/routetree/pattern_test.go`: the tests below, which fail until you're done.
- `internal/routetree/pattern.go` containing:
  - the `pathError` type (complete, don't change it)
  - a stub `splitPath` that returns `nil, errNotImplemented`

**You write the body of `splitPath`, and nothing else.** Don't change the tests to make them pass. If you think a test is wrong, say so and we'll discuss it.

```go
// pathError reports a problem in a pattern's path. (Provided.)
type pathError struct {
    Offset int    // byte index in the input where the problem is
    Reason string // short and human-readable
}

func (e *pathError) Error() string { ... } // provided

// splitPath splits the path part of a route pattern into its segments.
// YOU IMPLEMENT THIS.
func splitPath(path string) ([]string, error)
```

## Requirements

| # | Requirement |
|---|---|
| R1 | The input must be non-empty and start with `/`. Otherwise return `*pathError` with `Offset: 0`. |
| R2 | The segments are the pieces of text **between** slashes, in order, without the slashes. |
| R3 | `"/"` (just the root) returns **one empty segment**: `[""]`. |
| R4 | A trailing slash produces a **final empty segment**: `"/a/"` → `["a", ""]`. That's how the trie tells `/a` and `/a/` apart. |
| R5 | An empty segment anywhere *except* the end (two slashes in a row) is an error: `*pathError` whose `Offset` is the index of the **second slash of the first `//` pair**. |
| R6 | Nothing else is interpreted. `{`, `}`, `%`, spaces and non-ASCII letters are ordinary characters here (step 2 handles `{id}`). |
| R7 | **No copying:** every returned segment must be a substring (slice) of the input, sharing its memory. A test checks this. |
| R8 | **One allocation** for valid input: the result slice, created at its final size up front, so `append` never has to grow it. A test checks this with `testing.AllocsPerRun`. Error paths aren't counted. |
| R9 | It must never panic, for any input. A fuzz test checks this. |
| R10 | Use a loop over the string, with `strings.IndexByte` and/or indexing. **Don't use `strings.Split`, `strings.SplitSeq` or `strings.FieldsFunc`** in the implementation. The tests use `strings.Split` as a reference to check you against; see "Differential testing" below. |

## Examples

| Input | Result |
|---|---|
| `"/"` | `[""]` |
| `"/users"` | `["users"]` |
| `"/users/"` | `["users", ""]` |
| `"/users/{id}"` | `["users", "{id}"]` |
| `"/users/{id}/posts/"` | `["users", "{id}", "posts", ""]` |
| `"/a/b/c"` | `["a", "b", "c"]` |
| `"/café/{x...}"` | `["café", "{x...}"]` |
| `"/a b/%41"` | `["a b", "%41"]` (R6: taken literally) |
| `""` | error, Offset 0 |
| `"users"` | error, Offset 0 |
| `"//"` | error, Offset 1 |
| `"/a//b"` | error, Offset 3 |
| `"/a//"` | error, Offset 3 |
| `"/a/b///c"` | error, Offset 5 (the first `//` pair) |

Work through a few by hand before coding. For `"/a//b"`, the characters are at indexes `0:'/' 1:'a' 2:'/' 3:'/' 4:'b'`. The first `//` pair is at 2–3, so the Offset is **3**.

## The tests you'll be making pass

| Test | Checks |
|---|---|
| `TestSplitPath` | every valid row of the examples table |
| `TestSplitPath_Errors` | every error row: error type, `Offset`, a non-empty `Reason` |
| `TestSplitPath_SegmentsShareInputMemory` | R7, using `unsafe.StringData` to compare where the bytes live |
| `TestSplitPath_OneAllocation` | R8 |
| `FuzzSplitPath` | R9, plus the differential check against `strings.Split` |

Run just these, inside the dev container:

```sh
make docker-shell
go test ./internal/routetree -run 'SplitPath' -v
go test ./internal/routetree -run '^$' -fuzz FuzzSplitPath -fuzztime 30s   # optional, once green
```

Then `make docker-report` and send me `test-report.json` as usual.

## Go concepts you'll use

- **Strings are read-only byte sequences.** `s[i]` is a **byte** (`uint8`), not a character. `/` is a single byte in UTF-8, and UTF-8 guarantees that byte never appears inside a multi-byte character like `é`. So scanning bytes for `/` is safe even with non-ASCII text (this is why R6's `café` example works).
- **Substrings share memory.** `s[a:b]` doesn't copy. It makes a new small string header (a pointer plus a length) pointing into the same bytes. That's how R7 can be true.
- **Slices: length vs capacity.** `make([]string, 0, n)` creates an empty slice with room for `n` items. `append` fills it without reallocating until it's full. If you guess `n` too small, `append` allocates a bigger array and copies, and R8's test catches that. Hint: how many segments does a valid path have, compared with the number of `/` in it?
- **`strings.IndexByte(s, c)`** returns the index of the first `c` in `s`, or `-1`. It's heavily optimised (assembly, SIMD on many CPUs). `strings.Count(s, "/")` counts occurrences.
- **Returning `(value, error)`.** On failure, return `nil, &pathError{...}`. Callers check `err != nil` first. Never return a half-filled slice alongside an error.
- **`&pathError{...}` and pointer receivers.** The error is returned as a pointer, and `Error()` has a pointer receiver, so the tests can use `errors.As(err, &pe)` to get at `Offset`.
- **Differential testing.** For valid input, `strings.Split(path[1:], "/")` happens to give the same segments. The fuzz test generates thousands of random paths and checks that your function agrees with it, and that it errors exactly when `Split`'s result has an empty segment anywhere but the end. When a well-tested reference exists, compare against it rather than guessing every edge case by hand.

## C and C++ comparison

| In this task | C / C++ | Difference worth noticing |
|---|---|---|
| Splitting on `/` | `strtok(buf, "/")` | `strtok` **writes `'\0'` into your buffer** (destroys the input), keeps hidden global state (not thread-safe), and **silently skips empty tokens**, so `"/a//b"` would quietly become `a`, `b` instead of an error. R5 exists because silently collapsing `//` is a classic source of path-matching bugs |
| Segments as substrings | `std::string_view` / `const char*` + length | In C++, a `string_view` **dangles** if the original string is freed: a use-after-free. In Go the garbage collector keeps the original alive as long as any substring points into it. Safe, but a tiny substring can keep a huge string in memory |
| `make([]string, 0, n)` | `std::vector<std::string_view> v; v.reserve(n);` | Same idea: allocate once, then fill |
| `len(s)` | `strlen(s)` | Go strings store their length, with no `'\0'` terminator. `len` is O(1); `strlen` walks the whole string. A Go string can even contain zero bytes |
| `s[i]` past the end | **undefined behaviour**: reads whatever is there (buffer over-read) | Go **panics** with "index out of range". Never silent. R9's fuzz test makes sure no input triggers that panic |
| Returning `(segments, err)` | `return -1` + `errno`, or an out-parameter, or exceptions | Go makes the error an ordinary second return value that you check right away |

## Hints

Try for at least 20 minutes before opening one. Each gives away more than the last. You can also ask me for one in chat.

<details>
<summary>Hint 1: think in positions, not pieces</summary>

Don't think "split the string". Think "where does each segment start and end?" A segment starts just after a `/` and ends just before the next `/`, or at the end of the string. Keep an integer `start`, and repeatedly find the next `/` at or after it.

</details>

<details>
<summary>Hint 2: sizing the slice, and finding the next slash</summary>

In a valid path, the number of segments equals the number of `/` characters. Check that against the examples table: `"/"` has 1 slash and 1 segment; `"/a/"` has 2 and 2. So `strings.Count` gives you the exact capacity for R8.

To find the next slash after position `start`, search the remaining part, `path[start:]`. Remember that `strings.IndexByte` then returns an index *relative to `start`*, not to the whole string. That's the off-by-one trap in this task.

</details>

<details>
<summary>Hint 3: the shape of the loop (no code)</summary>

1. Check R1 first. Return the error immediately if it fails.
2. Allocate the result with the capacity from hint 2. Set `start` to just after the leading slash.
3. Loop:
   - Look for the next `/` in what remains.
   - **None found:** the rest of the string is the last segment (possibly empty, which is R3/R4). Add it, and you're done.
   - **Found one:** the segment runs from `start` up to that slash. If it's **empty**, you've found a `//`, and the slash you just found is the second of the pair: that's R5's Offset. Otherwise add the segment, move `start` past the slash, and go round again.
4. Ask yourself why an empty segment found *inside* the loop is always an error, while an empty segment found at the very end never is.

</details>

## Done when

- [ ] All `SplitPath` tests pass: `go test ./internal/routetree -run SplitPath`
- [ ] `make docker-test` passes (every package, race detector on)
- [ ] `gofmt` leaves the file unchanged and `go vet ./...` is clean
- [ ] You can explain your loop out loud, line by line
- [ ] Committed under **your** name, `feat(routetree): implement splitPath (green)`, and pushed to `milestone-2/router`
- [ ] You've told me it's pushed, and sent `test-report.json`. I'll review it like a PR review

## Explain it back (for your notes or an interview)

1. Why does `"/"` produce `[""]` and not `[]`? What would break in the trie if it produced `[]`?
2. Why is `"/a//b"` an error instead of being treated like `"/a/b"`? What could go wrong in a security-focused router if it were silently collapsed?
3. Your segments share memory with the input. In C++ that would be risky. Why isn't it in Go, and what is the one cost?
4. How did you know the slice's capacity in advance, and what would `testing.AllocsPerRun` have reported if you'd guessed too small?
5. Why does it matter that `/` can never appear inside a multi-byte UTF-8 character?

## Stretch goals (optional)

- Add `BenchmarkSplitPath` comparing your function with `strings.Split(path[1:], "/")` on a 6-segment path. Which is faster, and why might they be close?
- Read the `firstSegment` function in Go's own router (`$(go env GOROOT)/src/net/http/routing_tree.go`). It splits one segment at a time, lazily, instead of all at once. When is that better?
