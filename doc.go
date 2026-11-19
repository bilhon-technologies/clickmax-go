// Package clickmax is the Go client for the Clickmax public API.
//
// Create one [Client] per API key with [New] and call operations through the
// resource services on it:
//
//	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"))
//	if err != nil {
//		log.Fatal(err)
//	}
//	tags, err := c.Tags.List(ctx)
//
// A Client is safe for concurrent use, and clients built with different keys
// share nothing, so several workspaces can be used side by side. Keep the key
// on a server: anyone holding it can call the API on behalf of its workspace.
//
// # Clickmax Apps
//
// A Clickmax App is installed by many workspaces. [ExchangeInstallCode] turns
// the code of an install into an installation, and [NewApp] builds a client
// for one installation from the app's client id and secret. That client
// requests, caches and renews installation tokens on its own:
//
//	c, err := clickmax.NewApp(clickmax.AppCredentials{
//		ClientID:       os.Getenv("CLICKMAX_CLIENT_ID"),
//		ClientSecret:   os.Getenv("CLICKMAX_CLIENT_SECRET"),
//		InstallationID: installationID,
//	})
//
// # Options
//
// [Option] values passed to [New] set the defaults of every request. The same
// options can be passed as the last arguments of any method, where they apply
// to that call only:
//
//	tags, err := c.Tags.List(ctx, clickmax.WithTimeout(5*time.Second))
//
// Canceling ctx stops the request, including the read of the response body.
//
// # Errors
//
// When a request fails, methods return an [*Error], whether the API answered
// with an error status or no usable response arrived. Branch on [Error.Type]
// for control flow and on [Error.Code] for specific cases:
//
//	var cerr *clickmax.Error
//	if errors.As(err, &cerr) && cerr.Type == clickmax.ErrorTypeRateLimit {
//		time.Sleep(cerr.RetryAfter)
//	}
//
// The SDK never retries a request on its own, except that a client built with
// [NewApp] sends a call once more with a new installation token after a 401.
//
// # Optional and null fields
//
// Optional request fields are pointers; [Ptr] builds one from a literal. Fields
// that also accept null use [Nullable]: leave it as the zero value to omit the
// field, set it to [Null] to send null, or to [NewNullable] to send a value.
//
// Lists and maps are not pointers. An optional list left nil is omitted and an
// empty slice sends []. A required list left nil is sent as null, which the API
// rejects, so pass an empty slice ([]T{}) when there are no items.
package clickmax
