package clickmax

import (
	"net/http"
	"time"
)

// Option changes a setting of a [Client]. Options passed to [New] become the
// defaults of every request made through the client. Options passed as the
// last arguments of a method apply over those defaults for that call only and
// never change the client.
type Option func(*settings)

// HTTPClient sends the HTTP requests of a [Client]. [*http.Client] implements
// it; supply your own to add tracing, logging, proxies or test doubles.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// settings is everything a request needs. The client keeps one copy and every
// call works on its own copy, so per-call options cannot leak into the client.
type settings struct {
	apiKey     string
	app        *installationTokens // set by NewApp instead of apiKey; shared by every copy
	baseURL    string
	authURL    string
	httpClient HTTPClient // nil means http.DefaultClient, looked up per request
	header     http.Header
	timeout    time.Duration
}

// with returns a copy of s with opts applied. The header map is cloned first
// because options write into it.
func (s settings) with(opts []Option) settings {
	if len(opts) == 0 {
		return s
	}
	s.header = s.header.Clone()
	for _, opt := range opts {
		if opt != nil {
			opt(&s)
		}
	}
	return s
}

// WithBaseURL sends requests to baseURL instead of [DefaultBaseURL]. Keys that
// do not start with "cxp_" and client secrets that do not start with "cxp_cs_"
// belong to other environments and need it. The URL must use http or https;
// trailing slashes are trimmed. An empty baseURL leaves the current value
// unchanged.
//
// Installation tokens of a client built with [NewApp] are issued for the
// scheme and host of its base URL. On a method call of such a client, a
// baseURL with another scheme or host fails the call with
// [ErrorTypeInvalidRequest] and code "invalid_argument", without sending
// anything; build another client with NewApp to reach that API.
func WithBaseURL(baseURL string) Option {
	return func(s *settings) {
		if baseURL != "" {
			s.baseURL = baseURL
		}
	}
}

// WithAuthURL sends the token requests of [NewApp] and [ExchangeInstallCode]
// to authURL, the root of the login server, instead of [DefaultAuthURL].
// Client secrets that do not start with "cxp_cs_" belong to other environments
// and need it, together with [WithBaseURL]. The URL must use http or https;
// trailing slashes are trimmed. An empty authURL leaves the current value
// unchanged.
//
// Clients built with [New] ignore it, and so do method calls: a client built
// with NewApp keeps the login server it was given.
func WithAuthURL(authURL string) Option {
	return func(s *settings) {
		if authURL != "" {
			s.authURL = authURL
		}
	}
}

// WithHTTPClient sends requests through client instead of
// [http.DefaultClient]. Passing nil goes back to http.DefaultClient.
//
// Installation token requests of a client built with [NewApp] use the
// HTTPClient given to NewApp, never one passed to a method call.
//
// A Timeout set on an [*http.Client] still applies, but when it fires the
// error has type [ErrorTypeNetwork]; use [WithTimeout] to get
// [ErrorTypeTimeout].
func WithHTTPClient(client HTTPClient) Option {
	return func(s *settings) {
		s.httpClient = client
	}
}

// WithHeader sets the header key to value, replacing any value set before for
// the same key (keys are case-insensitive). On a method call it overrides the
// client's value for that call only.
//
// Authorization and X-Clickmax-Sdk cannot be changed this way: every request
// carries the client's credentials and the SDK version. Token requests to the
// login server do not carry these headers.
func WithHeader(key, value string) Option {
	return func(s *settings) {
		if s.header == nil {
			s.header = http.Header{}
		}
		s.header.Set(key, value)
	}
}

// WithTimeout limits how long a request may take, from sending it to reading
// the whole response body. The default is 60 seconds and 0 disables the limit.
// A request that runs out of time fails with [ErrorTypeTimeout].
//
// On a client built with [NewApp], the limit of a method call also covers
// waiting for an installation token and the retry after a 401. The token
// request itself is limited by the timeout given to NewApp, or by 60 seconds
// when that timeout is 0, so a login server that never answers cannot hold
// every later call. [ExchangeInstallCode] runs on the caller's ctx and keeps 0
// as no limit.
//
// A negative d makes [New] and [NewApp] return an error. On a method call or
// [ExchangeInstallCode] it fails that call with [ErrorTypeInvalidRequest] and
// code "invalid_argument", without sending anything.
func WithTimeout(d time.Duration) Option {
	return func(s *settings) {
		s.timeout = d
	}
}
