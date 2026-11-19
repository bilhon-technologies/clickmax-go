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

The package name is `clickmax`. Operations are grouped by resource: `c.<Resource>.<Operation>(ctx, [input,] options...)`. Operations without a resource, such as `c.Whoami(ctx)`, are methods on the client itself. Only older API keys can call `Whoami`, and it returns the workspace owner; apps and keys created with chosen permissions get `principal_not_allowed`, so check those credentials with a call their permissions cover, such as `c.Tags.List(ctx)`. The [package documentation](https://pkg.go.dev/clickmax.io/go) lists them all with their descriptions, and the [API reference](https://docs.clickmax.io/en/api-reference) documents each one.

Read operations take a params struct with the query parameters and write operations take the request body:

```go
c.Tags.ManualList(ctx, clickmax.TagsManualListParams{Name: clickmax.Ptr("vip"), PerPage: clickmax.Ptr[int64](50)})
c.Tags.Create(ctx, clickmax.TagsCreateInput{Name: "VIP", Color: clickmax.Ptr("ff8800")})
```

> [!WARNING]
> An API key acts on behalf of its whole workspace. Use it only on a server and load it from an environment variable or a secret manager. Never ship it in a client application.

## Clickmax Apps

An API key belongs to one workspace. A Clickmax App is installed by many workspaces, and each installation grants it the permissions an administrator of that workspace approved. Create the app in your workspace settings to get a client id and a client secret (`cxp_cs_...`, shown once).

The client for one installation takes the app credentials instead of a key:

```go
c, err := clickmax.NewApp(clickmax.AppCredentials{
	ClientID:       os.Getenv("CLICKMAX_CLIENT_ID"),
	ClientSecret:   os.Getenv("CLICKMAX_CLIENT_SECRET"),
	InstallationID: workspace.ClickmaxInstallationID,
})
if err != nil {
	return err
}

tags, err := c.Tags.List(ctx)
```

The client exchanges the credentials for an installation token on the first call and sends it as the bearer token. A token lasts 10 minutes. The client reuses it until one minute before it expires, and concurrent calls that need a new one wait on a single token request. When the API answers 401, the client requests a new token and sends that call once more; a second 401 is returned as the error. Token requests use the `WithHTTPClient` and `WithTimeout` passed to `NewApp`, never the ones passed to a method call; with `WithTimeout(0)` a token request is still limited to 60 seconds. Canceling a call's `ctx` stops only that call's wait: the token request goes on for the other calls waiting on it.

Each client keeps its own token, so build one client per installation and reuse it. The token is issued for the API given to `NewApp`, so a per-call base URL with a different scheme or host fails the call with code `invalid_argument`.

When the login server refuses the credentials, the call fails with `ErrorTypeAuthentication`, the HTTP status of the token response, and the OAuth error as `Code`:

|`Code`|Meaning|
|-|-|
|`invalid_client`|Wrong client id or secret, the secret was rotated, or the app was disabled or deleted.|
|`invalid_grant`|The installation was removed or belongs to another app, or the app is private and the installation is in another workspace.|
|`unauthorized_client`|The client is not a Clickmax App.|
|`invalid_request`, `invalid_target`|Malformed `InstallationID`, or a base URL that is not a Clickmax API.|

### Installing an app

A workspace administrator installs your app from a link to the Clickmax authorize endpoint. Generate a PKCE verifier and a `state` value for each link and keep both in the person's session:

```go
verifier := make([]byte, 32)
rand.Read(verifier) // crypto/rand; never fails since Go 1.24
codeVerifier := base64.RawURLEncoding.EncodeToString(verifier)
challenge := sha256.Sum256([]byte(codeVerifier))
state := rand.Text()

link := "https://login.clickmax.io/api/auth/oauth2/authorize?" + url.Values{
	"response_type":         {"code"},
	"client_id":             {os.Getenv("CLICKMAX_CLIENT_ID")},
	"redirect_uri":          {"https://app.example.com/clickmax/callback"},
	"scope":                 {"openid email install"},
	"state":                 {state},
	"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
	"code_challenge_method": {"S256"},
}.Encode()
```

The redirect URI must be one of the app's registered URIs: `https`, or `http` on `localhost`, `127.0.0.1` or `[::1]` for local development. The person signs in, picks the workspace, and reviews the permissions. Clickmax then redirects to your URI with `code` and `state`. When the callback carries `error` (such as `access_denied`, when the person declined) instead of `code`, the app was not installed and there is nothing to exchange. Otherwise check `state` against the session and exchange the code from your callback handler:

```go
func callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sess := sessions.Get(r)
	if q.Get("code") == "" || q.Get("state") != sess.State {
		http.Error(w, "app not installed", http.StatusBadRequest)
		return
	}

	out, err := clickmax.ExchangeInstallCode(r.Context(), clickmax.InstallCodeExchangeInput{
		ClientID:     os.Getenv("CLICKMAX_CLIENT_ID"),
		ClientSecret: os.Getenv("CLICKMAX_CLIENT_SECRET"),
		Code:         q.Get("code"),
		CodeVerifier: sess.CodeVerifier,
		RedirectURI:  "https://app.example.com/clickmax/callback",
	})
	if err != nil {
		http.Error(w, "install failed", http.StatusBadGateway)
		return
	}
	db.SaveInstallation(r.Context(), out.WorkspaceID, out.InstallationID)
}
```

Store `InstallationID` with the workspace and pass it to `NewApp` for every call on its behalf. Installing again in the same workspace returns the same installation. `IDToken` is set when the scope includes `openid`; it identifies the person who installed the app and arrives straight from the token endpoint over TLS, so you can read its claims without verifying the signature. A code is single use and expires quickly; refusals fail with `ErrorTypeAuthentication` and code `invalid_grant`. A code from an authorization without the `install` scope fails with code `not_an_install`.

### Sign in with Clickmax

For signing people in to your app, use any OpenID Connect library with discovery at `https://login.clickmax.io/api/auth/.well-known/openid-configuration` and the scopes `openid email profile`. ID tokens are signed with EdDSA (Ed25519); libraries that accept only RS256 need that algorithm enabled. Sign-in grants no access to the API.

### Limits of apps

An app acts with the permissions its installation holds, and no person is attached to its calls. When an installer could not grant every requested permission, the rest waits for an administrator's approval, and operations that need it fail with `ErrorTypePermission` and code `missing_permission` until then. Apps cannot use:

- operations for webhooks, social publishing, AI agents and webinars, and operations that are not governed by a permission you can grant, such as helpdesk: they fail with `ErrorTypePermission` and code `principal_not_allowed`;
- the Firecrawl and Apollo operations that call the provider (search, scrape, credit usage, health, Apollo lookups), which fail with `ErrorTypeNotFound`; connect, status and disconnect work;
- sending through connected WhatsApp, Instagram or email accounts, which belong to people;
- reads limited to the caller's own records: request the "all records" read permission;
- the permissions to manage apps and API keys, which no app or key can hold.

Workspace API keys with chosen permissions have the same limits. Keys created before permissions could be chosen act as the person who created them.

## Optional and null fields

Optional fields are pointers, and `clickmax.Ptr` builds one from a value. A nil pointer leaves an optional field out of the request. Required fields that accept `null` are pointers too, and nil sends `null`.

Lists and maps are not pointers. An optional list left nil is left out, and an empty slice sends `[]`. A required list left nil is sent as `null`, which the API rejects, so pass an empty slice when there are no items.

Fields that accept `null` as well as a value, usually to clear something, have the type `clickmax.Nullable[T]`. Its zero value leaves the field out, `clickmax.Null[T]()` sends `null` and `clickmax.NewNullable(v)` sends `v`. In responses, optional and null fields are nil pointers, slices or maps.

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
|`Capability`|Permission the key lacks, for `missing_permission`.|
|`Metric`|Limit that was reached, for `plan_limit_reached`.|
|`UpstreamStatus`|Status returned by the service behind the API, for `upstream_error`.|
|`Raw`|Response body as returned by the API.|
|`Err`|Underlying cause, also reachable through `errors.Is` and `errors.As`.|

|`Type`|When|
|-|-|
|`ErrorTypeInvalidRequest`|The input was rejected (400, 405, 413, 415, 422).|
|`ErrorTypeAuthentication`|The key is missing, invalid or revoked (401), or the login server refused the app credentials.|
|`ErrorTypePlanLimit`|The workspace reached a limit of its plan (402).|
|`ErrorTypePermission`|The key lacks a permission for this operation (403).|
|`ErrorTypeNotFound`|The resource does not exist (404, 410).|
|`ErrorTypeConflict`|The request conflicts with the current state (409, 412).|
|`ErrorTypeRateLimit`|Too many requests (429). Wait `RetryAfter`.|
|`ErrorTypeAPI`|Clickmax or an upstream service failed (408, 5xx).|
|`ErrorTypeNetwork`|The request failed before a response arrived.|
|`ErrorTypeTimeout`|The request took longer than the timeout, or `ctx` reached its deadline.|
|`ErrorTypeAborted`|`ctx` was canceled.|
|`ErrorTypeParse`|A successful response could not be decoded or lacks a required field.|

When the SDK rejects an argument before sending the request, such as a negative timeout or a body that cannot be encoded as JSON, the error has type `ErrorTypeInvalidRequest`, code `invalid_argument` and status `0`. `New` and `NewApp` return a plain error when the key or an app credential is empty or malformed, or when an option is invalid.

The SDK retries only the 401 of an app client, once, with a new token. Retrying on rate limits:

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

Canceling `ctx` stops the request. `Authorization` always comes from the client's key or app credentials.

With app credentials, `WithAuthURL` sets the login server that issues installation tokens. It defaults to `https://login.clickmax.io/api/auth` (`clickmax.DefaultAuthURL`) and is also an option of `ExchangeInstallCode`.

## Several workspaces

A `Client` is safe for concurrent use, and clients share no state. Create one per workspace (an app creates one per installation with `NewApp`):

```go
clients := map[string]*clickmax.Client{}
for workspace, key := range keys {
	c, err := clickmax.New(key)
	if err != nil {
		return fmt.Errorf("workspace %s: %w", workspace, err)
	}
	clients[workspace] = c
}
```

## Stability

The API is at version 0 (`/api/v0`) and this module is at `v0`. Operations and fields can still change between releases, and each release matches the API at the time it was published. Pin the version you tested against and read the release notes before upgrading.

## Links

- [API overview, authentication and errors](https://docs.clickmax.io/en/reference/api)
- [Interactive API reference](https://docs.clickmax.io/en/api-reference)
- [Package documentation](https://pkg.go.dev/clickmax.io/go)

## License

[Apache-2.0](./LICENSE)
