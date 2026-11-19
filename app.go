package clickmax

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultAuthURL is the production login server. [NewApp] and
// [ExchangeInstallCode] request tokens from it unless [WithAuthURL] gives
// another one.
const DefaultAuthURL = "https://login.clickmax.io/api/auth"

const installationGrantType = "urn:clickmax:params:oauth:grant-type:installation"

// A token is replaced this long before it expires, so a request signed with it
// does not reach the API after the expiry because of clock skew or a slow
// network. Tokens shorter than twice the margin are reused for half their
// lifetime instead.
const tokenRefreshMargin = time.Minute

// Ceiling on expires_in (Clickmax issues tokens for 10 minutes), so a malformed
// response cannot pin one token in the cache for good.
const maxTokenLifetime = 24 * time.Hour

// The environment letters are the same as for API keys (keyEnvironments).
var clientSecretPattern = regexp.MustCompile(`^cx([a-z])_cs_[A-Za-z0-9_-]{43}$`)

// AppCredentials identify a Clickmax App and the installation it acts for.
// Pass them to [NewApp].
type AppCredentials struct {
	// ClientID is the OAuth client id of the app, shown in the app settings.
	ClientID string
	// ClientSecret is the client secret of the app: "cxp_cs_" followed by 43
	// characters, shown once when the app is created or the secret is
	// rotated. Like the prefix of an API key, the letter after "cx" names the
	// environment the secret was issued in.
	ClientSecret string
	// InstallationID is the installation of the app in one workspace, returned
	// by [ExchangeInstallCode] when the workspace installed the app.
	InstallationID string
}

// NewApp returns a client of a Clickmax App that acts in the workspace where
// creds.InstallationID is installed, with the permissions that installation
// holds. Build one client per installation and reuse it: each client keeps
// its own installation token.
//
// The client requests an installation token from the login server on its first
// call and sends it as the bearer token. It reuses the token until one minute
// before it expires, and concurrent calls that need a new one wait on a single
// token request. When the API answers 401, the client requests a new token and
// sends that call once more; a second 401 is returned. Token requests use the
// [WithHTTPClient] and [WithTimeout] given to NewApp, never the ones passed to
// a method call, and a caller that gives up waiting does not cancel the token
// request for the others.
//
// When the login server refuses the credentials or the installation, the call
// fails with [ErrorTypeAuthentication], the status of the token response and
// the OAuth error as [Error.Code], such as "invalid_client" or
// "invalid_grant".
//
// NewApp makes no request. It returns an error when a credential is empty or
// malformed, when an option is invalid, or when a client secret issued for
// another environment ("cxd_cs_", "cxs_cs_", "cxt_cs_") lacks [WithBaseURL]
// or [WithAuthURL].
func NewApp(creds AppCredentials, opts ...Option) (*Client, error) {
	cfg := settings{header: http.Header{}, timeout: defaultTimeout}.with(opts)
	endpoint, env, err := appTokenEndpoint(creds.ClientID, creds.ClientSecret, cfg)
	if err != nil {
		return nil, fmt.Errorf("clickmax: %w", err)
	}
	if creds.InstallationID == "" {
		return nil, errors.New("clickmax: missing InstallationID")
	}
	// Apps never read the host override New reads for API keys: nothing
	// overrides their auth URL, so the override could send installation tokens
	// to an API their login server did not issue them for.
	baseURL, err := env.url(cfg.baseURL, DefaultBaseURL, "base URL", "WithBaseURL")
	if err != nil {
		return nil, fmt.Errorf("clickmax: %w", err)
	}
	cfg.baseURL = baseURL
	cfg.app = &installationTokens{
		endpoint:       endpoint,
		installationID: creds.InstallationID,
		resource:       apiResource(baseURL),
		now:            time.Now,
	}
	return newClient(cfg), nil
}

// InstallCodeExchangeInput holds what [ExchangeInstallCode] needs: the app
// credentials and the values of the install authorization.
type InstallCodeExchangeInput struct {
	// ClientID is the OAuth client id of the app.
	ClientID string
	// ClientSecret is the client secret of the app ("cxp_cs_..."). Its prefix
	// selects the environment, as in [AppCredentials].
	ClientSecret string
	// Code is the code query parameter Clickmax sent to the redirect URI.
	Code string
	// CodeVerifier is the PKCE verifier whose S256 challenge went into the
	// authorize URL.
	CodeVerifier string
	// RedirectURI is the redirect URI exactly as sent in the authorize URL.
	RedirectURI string
}

// InstallCodeExchange is the installation an install code created.
type InstallCodeExchange struct {
	// InstallationID is the [AppCredentials.InstallationID] for every call on
	// behalf of the workspace. Store it with the workspace. Installing the app
	// again in the same workspace returns the same installation.
	InstallationID string
	// WorkspaceID is the workspace the app was installed in.
	WorkspaceID string
	// IDToken is the OpenID Connect ID token of the person who installed the
	// app, or "" when the authorization did not ask for the openid scope. It
	// arrives straight from the token endpoint over TLS and the SDK does not
	// verify its signature.
	IDToken string
}

// ExchangeInstallCode exchanges the authorization code of an install for the
// installation it created. Call it from the handler of the redirect URI, then
// store InstallationID with the workspace and pass it to [NewApp].
//
// It sends one request to the login server. Canceling ctx stops it. Only
// [WithAuthURL], [WithHTTPClient] and [WithTimeout] apply; other options are
// ignored.
//
// The returned error is an [*Error]. A refused code, such as one that expired
// or was already used, fails with [ErrorTypeAuthentication] and code
// "invalid_grant"; refused credentials fail with code "invalid_client". A code
// from an authorization without the install scope fails with
// [ErrorTypeInvalidRequest] and code "not_an_install". Empty or malformed
// arguments fail with [ErrorTypeInvalidRequest] and code "invalid_argument",
// without sending anything.
func ExchangeInstallCode(ctx context.Context, in InstallCodeExchangeInput, opts ...Option) (*InstallCodeExchange, error) {
	cfg := settings{header: http.Header{}, timeout: defaultTimeout}.with(opts)
	endpoint, _, err := appTokenEndpoint(in.ClientID, in.ClientSecret, cfg)
	if err != nil {
		return nil, invalidArgument(err)
	}
	for _, arg := range []struct{ name, value string }{
		{"Code", in.Code},
		{"CodeVerifier", in.CodeVerifier},
		{"RedirectURI", in.RedirectURI},
	} {
		if arg.value == "" {
			return nil, invalidArgument(fmt.Errorf("missing %s", arg.name))
		}
	}

	status, body, cerr := postToken(ctx, endpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {in.Code},
		"code_verifier": {in.CodeVerifier},
		"redirect_uri":  {in.RedirectURI},
	})
	if cerr != nil {
		return nil, cerr
	}
	// No Raw in these errors: the body carries the person's access and ID
	// tokens.
	_, hasInstallation := body["installation_id"]
	_, hasWorkspace := body["workspace_id"]
	if !hasInstallation && !hasWorkspace {
		// A code from a sign-in without the install scope exchanges fine but
		// creates nothing.
		return nil, &Error{
			Type:    ErrorTypeInvalidRequest,
			Code:    "not_an_install",
			Status:  status,
			Message: "token response has no installation_id; the authorization did not ask for the install scope",
		}
	}
	out := &InstallCodeExchange{
		InstallationID: stringField(body, "installation_id"),
		WorkspaceID:    stringField(body, "workspace_id"),
		IDToken:        stringField(body, "id_token"),
	}
	if out.InstallationID == "" || out.WorkspaceID == "" {
		return nil, &Error{
			Type:    ErrorTypeParse,
			Code:    string(ErrorTypeParse),
			Status:  status,
			Message: "token response has an empty or invalid installation_id or workspace_id",
		}
	}
	return out, nil
}

// secretEnvironment is the environment a client secret was issued in.
type secretEnvironment struct {
	letter string
	name   string // "" for production
}

// url returns requested, or fallback when requested is empty. Non-production
// secrets must be given their URL: the production servers answer them with a
// plain refusal that hides the actual mistake.
func (e secretEnvironment) url(requested, fallback, name, option string) (string, error) {
	if requested == "" {
		if e.name != "" {
			return "", fmt.Errorf(`"cx%s_cs_" client secrets belong to the %s environment and do not work against %s; pass the matching %s with %s`, e.letter, e.name, fallback, name, option)
		}
		requested = fallback
	}
	return normalizeURL(name, requested)
}

// tokenEndpoint is where and how token requests are sent.
type tokenEndpoint struct {
	url          string
	clientID     string
	clientSecret string
	httpClient   HTTPClient // nil means http.DefaultClient
	timeout      time.Duration
}

// appTokenEndpoint checks the app credentials and options shared by NewApp and
// ExchangeInstallCode. Errors have no "clickmax: " prefix; callers add it or
// wrap them in an *Error.
func appTokenEndpoint(clientID, clientSecret string, cfg settings) (tokenEndpoint, secretEnvironment, error) {
	if clientID == "" {
		return tokenEndpoint{}, secretEnvironment{}, errors.New("missing ClientID")
	}
	if clientSecret == "" {
		return tokenEndpoint{}, secretEnvironment{}, errors.New("missing client secret")
	}
	var env secretEnvironment
	known := false
	if m := clientSecretPattern.FindStringSubmatch(clientSecret); m != nil {
		env.letter = m[1]
		env.name, known = keyEnvironments[env.letter]
	}
	if !known {
		return tokenEndpoint{}, secretEnvironment{}, errors.New(`malformed client secret; expected "cxp_cs_" followed by 43 characters`)
	}
	authURL, err := env.url(cfg.authURL, DefaultAuthURL, "auth URL", "WithAuthURL")
	if err != nil {
		return tokenEndpoint{}, secretEnvironment{}, err
	}
	if cfg.timeout < 0 {
		return tokenEndpoint{}, secretEnvironment{}, errNegativeTimeout
	}
	return tokenEndpoint{
		url:          authURL + "/oauth2/token",
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   cfg.httpClient,
		timeout:      cfg.timeout,
	}, env, nil
}

// apiResource is the audience of installation tokens: the origin of the API
// followed by /api, whatever version path the base URL carries. Host case and
// default ports are normalized as the WHATWG URL parser does, so the value
// matches what other Clickmax SDKs send.
func apiResource(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	if (u.Scheme == "https" && strings.HasSuffix(host, ":443")) || (u.Scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndexByte(host, ':')]
	}
	return u.Scheme + "://" + host + "/api"
}

// installationTokens keeps the installation token of one client: reused until
// tokenRefreshMargin before it expires, and requested once for every call
// waiting on it.
type installationTokens struct {
	endpoint       tokenEndpoint
	installationID string
	resource       string
	now            func() time.Time // replaced in tests

	mu        sync.Mutex
	token     string // "" when none is cached
	refreshAt time.Time
	pending   *tokenRequest
}

// tokenRequest is one token request shared by every call waiting on it. token
// and err are set before done is closed.
type tokenRequest struct {
	done  chan struct{}
	token string
	err   *Error
}

// get returns the cached token while it is fresh, otherwise the result of the
// request every caller shares. ctx bounds only this caller's wait.
func (t *installationTokens) get(ctx context.Context) (string, error) {
	// A canceled call does not start or join a request it would not wait for.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	t.mu.Lock()
	if t.token != "" && t.now().Before(t.refreshAt) {
		token := t.token
		t.mu.Unlock()
		return token, nil
	}
	req := t.pending
	if req == nil {
		req = &tokenRequest{done: make(chan struct{})}
		t.pending = req
		go t.fetch(req)
	}
	t.mu.Unlock()

	select {
	case <-req.done:
		if req.err != nil {
			// A copy per caller, so one caller changing its error cannot
			// change another's.
			e := *req.err
			return "", &e
		}
		return req.token, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// renew drops rejected, a token the API answered 401 to, and returns a
// replacement. Another call may already have replaced it; that replacement is
// reused instead of requesting a third token.
func (t *installationTokens) renew(ctx context.Context, rejected string) (string, error) {
	t.mu.Lock()
	if t.token == rejected {
		t.token = ""
	}
	t.mu.Unlock()
	return t.get(ctx)
}

// fetch runs detached from every caller, so one caller giving up does not fail
// the others waiting on the same request. The client's timeout bounds it.
func (t *installationTokens) fetch(req *tokenRequest) {
	// Measured before the request, so time in flight counts against the token.
	startedAt := t.now()
	token, lifetime, err := t.request()
	t.mu.Lock()
	// A failure is not cached: the next call tries again.
	if err == nil {
		t.token = token
		t.refreshAt = startedAt.Add(lifetime - min(tokenRefreshMargin, lifetime/2))
	}
	t.pending = nil
	t.mu.Unlock()
	req.token, req.err = token, err
	close(req.done)
}

func (t *installationTokens) request() (string, time.Duration, *Error) {
	status, body, err := postToken(context.Background(), t.endpoint, url.Values{
		"grant_type":      {installationGrantType},
		"installation_id": {t.installationID},
		"resource":        {t.resource},
	})
	if err != nil {
		return "", 0, err
	}
	token := stringField(body, "access_token")
	if token == "" {
		// No Raw: the body may carry other tokens, and errors end up in logs.
		return "", 0, &Error{
			Type:    ErrorTypeParse,
			Code:    string(ErrorTypeParse),
			Status:  status,
			Message: "token response has no access_token",
		}
	}
	return token, tokenLifetime(body), nil
}

// tokenLifetime reads expires_in. Without a usable value the token serves only
// the calls already waiting on it.
func tokenLifetime(body map[string]json.RawMessage) time.Duration {
	var seconds float64
	if json.Unmarshal(body["expires_in"], &seconds) != nil || seconds <= 0 {
		return 0
	}
	if seconds >= maxTokenLifetime.Seconds() {
		return maxTokenLifetime
	}
	return time.Duration(seconds * float64(time.Second))
}

// postToken sends one form-encoded request to the token endpoint with HTTP
// Basic client authentication and returns the status and JSON object of a 2xx
// response.
func postToken(ctx context.Context, endpoint tokenEndpoint, form url.Values) (int, map[string]json.RawMessage, *Error) {
	reqCtx, cancel := ctx, context.CancelFunc(func() {})
	if endpoint.timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, endpoint.timeout)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint.url, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, invalidArgument(err)
	}
	// RFC 6749 section 2.3.1: id and secret are form-encoded before Base64.
	basic := base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(endpoint.clientID) + ":" + url.QueryEscape(endpoint.clientSecret)))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "clickmax-go/"+Version)

	client := endpoint.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, transportError(ctx, reqCtx, endpoint.timeout, err)
	}
	var data []byte
	if resp.Body != nil {
		data, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return 0, nil, transportError(ctx, reqCtx, endpoint.timeout, err)
		}
	}

	// A body that is not a JSON object leaves body nil.
	var body map[string]json.RawMessage
	_ = json.Unmarshal(data, &body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, nil, tokenError(resp.StatusCode, resp.Header, data, body)
	}
	if body == nil {
		// No Raw, as for every successful token response: it may hold
		// credentials.
		return resp.StatusCode, nil, &Error{
			Type:    ErrorTypeParse,
			Code:    string(ErrorTypeParse),
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("token response with status %d is not a JSON object", resp.StatusCode),
		}
	}
	return resp.StatusCode, body, nil
}

// tokenError maps a failed token response. OAuth refusals ({"error",
// "error_description"}, RFC 6749 section 5.2) mean the credentials, the
// installation or the code were refused, so they become authentication errors
// with the OAuth error as Code. Rate limits, server errors and bodies in
// another shape follow the API's status mapping.
func tokenError(status int, header http.Header, data []byte, body map[string]json.RawMessage) *Error {
	oauthErr := stringField(body, "error")
	if oauthErr == "" || status == http.StatusTooManyRequests || status >= 500 {
		return errorFromResponse(status, header, data)
	}
	return &Error{
		Type:    ErrorTypeAuthentication,
		Code:    oauthErr,
		Status:  status,
		Message: firstNonEmpty(stringField(body, "error_description"), oauthErr),
		Raw:     data,
	}
}
