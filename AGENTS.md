# clickmax-go

Public Go module `clickmax.io/go` (package `clickmax`, Apache-2.0) = code generated from the Clickmax OpenAPI document (`openapi.yaml`, copy of `https://docs.clickmax.io/openapi.yaml`) + a small hand-written runtime. This repo is public: no internal hostnames, environments, endpoints or private repo references in code, comments, docs, commits or workflows.

## Rules

- `*.gen.go` = generator output. NEVER edit by hand; change `internal/generator` or the spec, then regenerate. CI fails when the committed output differs from a fresh run.
- Names come from the spec path: `/<resource>/<action>` -> `c.<Pascal(resource)>.<Pascal(action)>()`, `/<name>` -> root `c.<Pascal(name)>()`. Bad names are fixed in the API, never aliased here. Component names are re-cased with the same initialism list (`internal/generator/names.go`).
- Every exported identifier, its doc comment and every error message is public API under semver. Removing or renaming a method, type, field, constant or `ErrorType` value is breaking; the initialism list is frozen for the same reason.
- Root module has zero dependencies (`go.mod` without `require`). The generator is a separate module (`internal/generator/go.mod`) so its dependencies never reach users.
- `go 1.24` floor = `omitzero` json tags. Raising it is a breaking change for users on older toolchains.
- Generated code depends on these runtime names: `settings`, `Option`, `(*Client).do`, `addString/addInt/addFloat/addBool/addTime`, `Nullable`. The generator reads the runtime files and fails on name collisions with them.
- Methods never retry and never validate input or output beyond JSON decoding (parity with the TypeScript SDK).
- Hand-written code: godoc on every exported identifier; comments explain why/when, never what.

## Type mapping tradeoffs

- Object unions (`anyOf` of objects) become one flattened struct with every variant's fields; a field is required only when every variant requires it. Conflicting variant types widen (enum union, nullable, `any`).
- Untyped `{}` and mixed unions (string|object, number|string) -> `any`; objects without properties -> `map[string]any`.
- Optional + nullable fields use `Nullable[T]` in types reachable from requests and pointers in response-only types.
- A required slice left nil is sent as `null`.
- Decoding is strict on types: a response that deviates from the spec (string where it says number) fails the call with `ErrorTypeParse`, with the body in `Raw`.

## Commands

```sh
go run -C internal/generator . -spec ../../openapi.yaml -out ../..   # regenerate
go vet ./... && go test ./...                                        # root module
(cd internal/generator && go vet ./... && go test ./...)             # generator
```

Not `go generate`: it lists package files before running the generator and fails when a removed resource's file is deleted mid-run.

## Releases

`.github/workflows/release.yml`, triggered by `repository_dispatch` `openapi-updated` (payload `{sha256, version}`) when a new spec is published, or manually (`workflow_dispatch`, `bump: minor|patch`) for runtime changes. It waits until the published spec matches `sha256`, regenerates, runs gofmt/vet/test, and releases only when the output or `main` changed since the last tag: next `v0.<minor+1>.0` (or patch), `version.go` rewritten, commit `Release vX.Y.Z`, tag, GitHub release, proxy warm-up. Version is independent from the API's version: a Go major >= 2 changes the import path (`clickmax.io/go/v2`).

`clickmax.io/go` resolves through a `go-import` meta tag served on `https://clickmax.io/go?go-get=1` pointing at `https://github.com/bilhon-technologies/clickmax-go`. The module path in `go.mod` must keep matching it.
