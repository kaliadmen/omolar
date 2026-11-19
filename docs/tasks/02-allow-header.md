# Task 2: `Allow` header for 405 responses

| | |
|---|---|
| **Owner** | Quinze writes the implementation. Claude wrote the failing tests and the stub, and reviews |
| **Milestone / step** | Milestone 2, step 8 ([design §6.4, §11.4](../design/02-router.md#64-methods)) |
| **File** | `router/allow.go` |
| **Difficulty** | ★☆☆ |
| **Time** | 20–60 minutes |

## Why this matters

When a path exists but not for the request's method (`DELETE /users/7` where only `GET` and `POST` are registered), HTTP says to answer **405 Method Not Allowed** with an `Allow` header listing the methods that would work (RFC 9110 §15.5.6). Clients and tools read it, so it has to be exact.

The tree already finds the methods: `routetree.Tree.Allowed` returns them **unsorted and possibly repeated**, because it collects from every route on the path. Your function turns that into the header value. After you're done, Claude wires it into `ServeHTTP` along with `WithMethodNotAllowed`.

```go
func allowHeader(methods []string) string // YOU IMPLEMENT THIS
```

## Requirements

| # | Requirement |
|---|---|
| R1 | Each method appears **once**. |
| R2 | Methods are **sorted** in byte order (`strings.Compare` order), so the output doesn't depend on input order. |
| R3 | If `GET` is present, `HEAD` is included too, because the router serves HEAD with GET routes. Don't add it twice if it's already there. |
| R4 | `HEAD` without `GET` stays `HEAD` alone. HEAD never implies GET. |
| R5 | Any other method (`PURGE`, `M-SEARCH`, `OPTIONS`...) is passed through as given. Nothing is added except R3's HEAD. |
| R6 | Joined with `", "` (comma and space): `"GET, HEAD, POST"`. |
| R7 | Empty or nil input returns `""`. |
| R8 | **Don't modify the input slice.** It belongs to the caller. |

## Examples

| Input | Output |
|---|---|
| `[GET]` | `"GET, HEAD"` |
| `[HEAD, GET]` | `"GET, HEAD"` |
| `[HEAD]` | `"HEAD"` |
| `[POST, PUT, DELETE]` | `"DELETE, POST, PUT"` |
| `[GET, POST, GET, POST, GET]` | `"GET, HEAD, POST"` |
| `[PUT, PATCH, POST, M-SEARCH, MKCOL]` | `"M-SEARCH, MKCOL, PATCH, POST, PUT"` (`-` sorts before letters) |
| `[]` or `nil` | `""` |

## Tests

`router/allow_test.go`: `TestAllowHeader` (the table above and more), `TestAllowHeader_DoesNotModifyInput` (R8), `TestAllowHeader_SameForAnyOrder` (R2).

```sh
make docker-shell
go test -race ./router -run AllowHeader -v
```

## Concepts

- **`slices` package** (Go 1.21+): `slices.Clone`, `slices.Sort`, `slices.Contains`, `slices.Compact`. Read what `Compact` does with **unsorted** input before relying on it.
- **Sorting in place vs a copy:** `slices.Sort(s)` rearranges `s` itself. That's R8's trap, the same one Task 4 had with `byMethod`.
- **`strings.Join`** builds the final string in one allocation.
- **Alternative:** a `map[string]bool` as a set removes duplicates, but map iteration order is random in Go, so you'd still have to sort the keys.
- **C++ comparison:** this is `std::sort` + `std::unique` + `erase`. `slices.Compact` is `std::unique`: both only remove *adjacent* duplicates, which is why sorting comes first. In C++ you'd take `const std::vector<std::string>&` to promise not to modify the input; Go has no `const`, so R8 is enforced by a test.

## Hints

<details>
<summary>Hint 1</summary>

Do the steps in an order where each makes the next easy: work on a copy → make sure HEAD is there when GET is → sort → remove duplicates → join.

</details>

<details>
<summary>Hint 2</summary>

If you add `HEAD` unconditionally whenever `GET` is present, you may end up with two `HEAD`s. That's fine *if* duplicate removal happens after sorting, because sorting puts the two next to each other.

</details>

## Done when

- [ ] `go test -race ./router -run AllowHeader` passes, and so does `make docker-test`
- [ ] `gofmt` and `go vet` are clean
- [ ] Committed under your name, `feat(router): implement allowHeader (green)`, pushed to `milestone-2/router`
- [ ] You've told me, and I'll review it and then wire up 405s

## Explain it back

1. Why must sorting come before `slices.Compact`?
2. Why is HEAD added for GET but never GET for HEAD?
3. What would go wrong if the function sorted its input in place, given where the input comes from?
