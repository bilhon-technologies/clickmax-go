# clickmax-go

Go client for the [Clickmax public API](https://docs.clickmax.io/en/reference/api). It is generated from the API's OpenAPI document, so every operation and its input and output types match the API version the module was released from.

```sh
go get clickmax.io/go
```

It needs Go 1.24 or later and has no dependencies outside the standard library.

## Quickstart

Create an API key for your workspace ([authentication guide](https://docs.clickmax.io/en/reference/api)), then:

```go
package main

import (
	"context"
	"log"
	"os"

	"clickmax.io/go"
)

func main() {
	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	tags, err := c.Tags.List(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d tags", len(tags.Data))
}
```

The package name is `clickmax`. Operations are grouped by resource: `c.<Resource>.<Operation>(ctx, input, options...)`. Operations without a resource, such as `c.Whoami(ctx)`, are methods on the client itself. The [package documentation](https://pkg.go.dev/clickmax.io/go) lists them all with their descriptions, and the [API reference](https://docs.clickmax.io/en/api-reference) documents each one.

Read operations take a params struct with the query parameters and write operations take the request body:

```go
c.Tags.ManualList(ctx, clickmax.TagsManualListParams{Name: clickmax.Ptr("vip"), PerPage: clickmax.Ptr(50.0)})
c.Tags.Create(ctx, clickmax.TagsCreateInput{Name: "VIP", Color: clickmax.Ptr("ff8800")})
```

> [!WARNING]
> An API key acts on behalf of its whole workspace. Use it only on a server and load it from an environment variable or a secret manager. Never ship it in a client application.

## Optional and null fields

Optional fields are pointers, and `clickmax.Ptr` builds one from a value. A nil pointer leaves the field out of the request.

Fields that accept `null` as well as a value, usually to clear something, have the type `clickmax.Nullable[T]`. Its zero value leaves the field out, `clickmax.Null[T]()` sends `null` and `clickmax.NewNullable(v)` sends `v`. In responses, optional and null fields are nil pointers.

## Errors

Every method returns an `error`. When a request fails it is a `*clickmax.Error`:

```go
out, err := c.Tags.Create(ctx, clickmax.TagsCreateInput{Name: "VIP"})
var cerr *clickmax.Error
if errors.As(err, &cerr) {
	log.Printf("tags create failed: type=%s code=%s request_id=%s", cerr.Type, cerr.Code, cerr.RequestID)
}
```

|Field|Description|
|-|-|
|`Type`|Category of the error, listed below. Use it for control flow.|
|`Code`|Specific reason in lower_snake case, such as `invalid_input`, `missing_permission` or `plan_limit_reached`.|
|`Status`|HTTP status, or `0` when no response was received.|
|`Message`|Description meant for logs. It may change between releases.|
|`RequestID`|Identifier of the request. Include it when you contact support.|
|`RetryAfter`|How long to wait before retrying, when the API sent `Retry-After`.|
|`Issues`|Field-level problems of an `invalid_input` error.|
|`Raw`|Response body as returned by the API.|
|`Err`|Underlying cause, also reachable through `errors.Is` and `errors.As`.|

|`Type`|When|
|-|-|
|`ErrorTypeInvalidRequest`|The input was rejected (400, 405, 413, 415, 422).|
|`ErrorTypeAuthentication`|The key is missing, invalid or revoked (401).|
|`ErrorTypePlanLimit`|The workspace reached a limit of its plan (402).|
|`ErrorTypePermission`|The key lacks a permission for this operation (403).|
|`ErrorTypeNotFound`|The resource does not exist (404, 410).|
|`ErrorTypeConflict`|The request conflicts with the current state (409, 412).|
|`ErrorTypeRateLimit`|Too many requests (429). Wait `RetryAfter`.|
|`ErrorTypeAPI`|Clickmax or an upstream service failed (408, 5xx).|
|`ErrorTypeNetwork`|The request failed before a response arrived.|
|`ErrorTypeTimeout`|The request took longer than the timeout, or `ctx` reached its deadline.|
|`ErrorTypeAborted`|`ctx` was canceled.|
|`ErrorTypeParse`|A successful response could not be decoded.|

When the SDK rejects an argument before sending the request, such as a negative timeout or a body that cannot be encoded as JSON, the error has type `ErrorTypeInvalidRequest`, code `invalid_argument` and status `0`. `New` returns a plain error when the key is empty or malformed, or when an option is invalid.

The SDK never retries. Retrying on rate limits:

```go
func listTags(ctx context.Context, c *clickmax.Client) (*clickmax.TagsListOutput, error) {
	for attempt := 0; ; attempt++ {
		out, err := c.Tags.List(ctx)
		var cerr *clickmax.Error
		if attempt == 3 || !errors.As(err, &cerr) || cerr.Type != clickmax.ErrorTypeRateLimit {
			return out, err
		}
		wait := cerr.RetryAfter
		if wait == 0 {
			wait = 10 * time.Second
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
```

## Options

```go
c, err := clickmax.New(apiKey,
	clickmax.WithHTTPClient(myInstrumentedClient), // default http.DefaultClient
	clickmax.WithHeader("X-Request-Source", "billing-sync"), // sent with every request
	clickmax.WithTimeout(30*time.Second), // default 60s, 0 disables
)
```

The same options can be passed as the last arguments of any method. They apply over the client's options for that call only:

```go
c.Tags.List(ctx, clickmax.WithTimeout(5*time.Second), clickmax.WithHeader("X-Request-Id", id))
```

Canceling `ctx` stops the request. `Authorization` always comes from the client's key.

## Several workspaces

A `Client` is safe for concurrent use, and clients share no state. Create one per workspace:

```go
storeA, err := clickmax.New(os.Getenv("STORE_A_KEY"))
storeB, err := clickmax.New(os.Getenv("STORE_B_KEY"))
```

## Stability

The API is at version 0 (`/api/v0`) and this module is at `v0`. Operations and fields can still change between releases, and each release matches the API at the time it was published. Pin the version you tested against and read the release notes before upgrading.

## Links

- [API overview, authentication and errors](https://docs.clickmax.io/en/reference/api)
- [Interactive API reference](https://docs.clickmax.io/en/api-reference)
- [Package documentation](https://pkg.go.dev/clickmax.io/go)

## License

[Apache-2.0](./LICENSE)
