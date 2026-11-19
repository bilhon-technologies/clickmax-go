package clickmax

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	prodSecret    = "cxp_cs_" + strings.Repeat("s", 43)
	stagingSecret = "cxs_cs_" + strings.Repeat("s", 43)
)

const (
	clientID       = "client 1:x" // needs form encoding in the Basic header
	installationID = "0b6f3c3e-1d1f-4a53-9a51-6f3d7f0a1a11"
	tokenURL       = "https://login.clickmax.io/api/auth/oauth2/token"
)

// fakeServers is a login server and an API behind one HTTPClient. By default
// the n-th token request gets token "t<n>" for 600 seconds and the API answers
// 200 to any token.
type fakeServers struct {
	token func(n int) (status int, body string)
	api   func(bearer string) (status int)

	mu         sync.Mutex
	tokenReqs  []*http.Request
	tokenForms []url.Values
	apiReqs    []*http.Request
	bearers    []string
}

func (f *fakeServers) Do(r *http.Request) (*http.Response, error) {
	status, body := http.StatusOK, `{"data":[]}`
	if strings.HasSuffix(r.URL.Path, "/oauth2/token") {
		data, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(data))
		f.mu.Lock()
		f.tokenReqs = append(f.tokenReqs, r)
		f.tokenForms = append(f.tokenForms, form)
		n := len(f.tokenReqs)
		f.mu.Unlock()
		body = fmt.Sprintf(`{"access_token":"t%d","token_type":"Bearer","expires_in":600}`, n)
		if f.token != nil {
			status, body = f.token(n)
		}
	} else {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		f.apiReqs = append(f.apiReqs, r)
		f.bearers = append(f.bearers, bearer)
		f.mu.Unlock()
		if f.api != nil {
			status = f.api(bearer)
		}
		if status == http.StatusUnauthorized {
			body = `{"type":"authentication","code":"unauthorized","error":"Unauthorized"}`
		}
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func (f *fakeServers) counts() (tokens int, bearers []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokenReqs), slices.Clone(f.bearers)
}

func newTestApp(t *testing.T, f *fakeServers, opts ...Option) *Client {
	t.Helper()
	c, err := NewApp(AppCredentials{ClientID: clientID, ClientSecret: prodSecret, InstallationID: installationID}, append(opts, WithHTTPClient(f))...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// parallel runs n calls at once and returns their errors.
func parallel(n int, call func() error) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = call()
		}()
	}
	wg.Wait()
	return errs
}

func TestAppTokenSharedAndRenewedBeforeExpiry(t *testing.T) {
	// The token answers only once every concurrent call is on its way to it.
	var calling sync.WaitGroup
	calling.Add(3)
	f := &fakeServers{token: func(n int) (int, string) {
		calling.Wait()
		return http.StatusOK, fmt.Sprintf(`{"access_token":"t%d","expires_in":600}`, n)
	}}
	c := newTestApp(t, f)
	start := time.Unix(1_700_000_000, 0)
	now := start
	c.cfg.app.now = func() time.Time { return now }

	for _, err := range parallel(3, func() error { calling.Done(); return get(c) }) {
		if err != nil {
			t.Fatal(err)
		}
	}

	now = start.Add(600*time.Second - 61*time.Second)
	if err := get(c); err != nil {
		t.Fatal(err)
	}
	now = start.Add(600*time.Second - 60*time.Second)
	if err := get(c); err != nil {
		t.Fatal(err)
	}

	tokens, bearers := f.counts()
	if want := []string{"t1", "t1", "t1", "t1", "t2"}; tokens != 2 || !reflect.DeepEqual(bearers, want) {
		t.Errorf("got %d token requests and bearers %v, want 2 and %v", tokens, bearers, want)
	}
	r := f.tokenReqs[0]
	if r.Method != http.MethodPost || r.URL.String() != tokenURL || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("token request sent as %s %s %q", r.Method, r.URL, r.Header.Get("Content-Type"))
	}
	if user, pass, ok := r.BasicAuth(); !ok || user != "client+1%3Ax" || pass != prodSecret {
		t.Errorf("Basic credentials = %q, %q, %v; want the form-encoded client id and the secret", user, pass, ok)
	}
	wantForm := url.Values{
		"grant_type":      {"urn:clickmax:params:oauth:grant-type:installation"},
		"installation_id": {installationID},
		"resource":        {"https://mcp.clickmax.io/api"},
	}
	if !reflect.DeepEqual(f.tokenForms[0], wantForm) {
		t.Errorf("token form = %v, want %v", f.tokenForms[0], wantForm)
	}
}

func TestAppRetriesOnceAfter401(t *testing.T) {
	t.Run("concurrent calls share the renewal", func(t *testing.T) {
		// Both calls are refused t1 before either renews, so both retry.
		// Bounded, so a client that never sends t1 twice fails instead of
		// hanging.
		var arrived sync.WaitGroup
		arrived.Add(2)
		bothArrived := make(chan struct{})
		go func() { arrived.Wait(); close(bothArrived) }()
		f := &fakeServers{api: func(bearer string) int {
			if bearer != "t1" {
				return http.StatusOK
			}
			arrived.Done()
			select {
			case <-bothArrived:
			case <-time.After(5 * time.Second):
			}
			return http.StatusUnauthorized
		}}
		c := newTestApp(t, f)

		for _, err := range parallel(2, func() error { return get(c) }) {
			if err != nil {
				t.Fatal(err)
			}
		}
		tokens, bearers := f.counts()
		slices.Sort(bearers)
		if want := []string{"t1", "t1", "t2", "t2"}; tokens != 2 || !reflect.DeepEqual(bearers, want) {
			t.Errorf("got %d token requests and bearers %v, want 2 and %v", tokens, bearers, want)
		}
	})

	refuseAll := func(string) int { return http.StatusUnauthorized }
	cases := []struct {
		name        string
		client      func(*fakeServers) (*Client, error)
		wantBearers []string
	}{
		{"app returns the second 401", func(f *fakeServers) (*Client, error) {
			return NewApp(AppCredentials{ClientID: clientID, ClientSecret: prodSecret, InstallationID: installationID}, WithHTTPClient(f))
		}, []string{"t1", "t2"}},
		{"API key never retries", func(f *fakeServers) (*Client, error) {
			return New(prodKey, WithHTTPClient(f))
		}, []string{prodKey}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeServers{api: refuseAll}
			c, err := tc.client(f)
			if err != nil {
				t.Fatal(err)
			}
			e := asError(t, get(c))
			if e.Type != ErrorTypeAuthentication || e.Status != http.StatusUnauthorized {
				t.Errorf("got %+v, want the API's 401", e)
			}
			if _, bearers := f.counts(); !reflect.DeepEqual(bearers, tc.wantBearers) {
				t.Errorf("bearers = %v, want %v", bearers, tc.wantBearers)
			}
		})
	}
}

func TestAppCallerGivingUpKeepsTheTokenRequest(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	f := &fakeServers{token: func(n int) (int, string) {
		close(started)
		<-release
		return http.StatusOK, `{"access_token":"t1","expires_in":600}`
	}}
	c := newTestApp(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan error, 1)
	go func() { canceled <- c.do(ctx, http.MethodGet, "/whoami", nil, nil, nil, nil) }()
	<-started
	kept := make(chan error, 1)
	go func() { kept <- get(c) }()

	cancel()
	err := <-canceled
	if e := asError(t, err); e.Type != ErrorTypeAborted || !errors.Is(err, context.Canceled) {
		t.Errorf("canceled call got %+v, want aborted", e)
	}
	close(release)
	if err := <-kept; err != nil {
		t.Fatalf("other caller failed: %v", err)
	}
	if tokens, bearers := f.counts(); tokens != 1 || !reflect.DeepEqual(bearers, []string{"t1"}) {
		t.Errorf("got %d token requests and bearers %v, want 1 and [t1]", tokens, bearers)
	}
}

func TestAppTokenFailures(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    Error
		wantRaw bool
	}{
		{"OAuth refusal with description", 400, `{"error":"invalid_grant","error_description":"installation not found"}`,
			Error{Type: ErrorTypeAuthentication, Code: "invalid_grant", Status: 400, Message: "installation not found"}, true},
		{"OAuth refusal without description", 401, `{"error":"invalid_client"}`,
			Error{Type: ErrorTypeAuthentication, Code: "invalid_client", Status: 401, Message: "invalid_client"}, true},
		{"server error follows the API mapping", 503, `{"error":"temporarily_unavailable"}`,
			Error{Type: ErrorTypeAPI, Code: "api", Status: 503, Message: "temporarily_unavailable"}, true},
		{"success without access_token", 200, `{"token_type":"Bearer","id_token":"secret"}`,
			Error{Type: ErrorTypeParse, Code: "parse", Status: 200, Message: "token response has no access_token"}, false},
		{"success that is not a JSON object", 200, `["t1"]`,
			Error{Type: ErrorTypeParse, Code: "parse", Status: 200, Message: "token response with status 200 is not a JSON object"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeServers{token: func(n int) (int, string) {
				if n == 1 {
					return tc.status, tc.body
				}
				return http.StatusOK, `{"access_token":"t2","expires_in":600}`
			}}
			c := newTestApp(t, f)

			e := asError(t, get(c))
			got := Error{Type: e.Type, Code: e.Code, Status: e.Status, Message: e.Message}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			if hasRaw := e.Raw != nil; hasRaw != tc.wantRaw {
				t.Errorf("Raw = %q, want it kept: %v", e.Raw, tc.wantRaw)
			}

			// The failure is not cached: the next call requests a token again.
			if err := get(c); err != nil {
				t.Fatal(err)
			}
			if tokens, bearers := f.counts(); tokens != 2 || !reflect.DeepEqual(bearers, []string{"t2"}) {
				t.Errorf("got %d token requests and bearers %v, want 2 and [t2]", tokens, bearers)
			}
		})
	}
}

func TestNewAppRejects(t *testing.T) {
	const (
		apiURL  = "https://mcp.example.test/api/v0"
		authURL = "https://login.example.test/api/auth"
	)
	creds := AppCredentials{ClientID: clientID, ClientSecret: prodSecret, InstallationID: installationID}
	with := func(edit func(*AppCredentials)) AppCredentials {
		c := creds
		edit(&c)
		return c
	}
	staging := with(func(c *AppCredentials) { c.ClientSecret = stagingSecret })
	cases := []struct {
		name  string
		creds AppCredentials
		env   string
		opts  []Option
	}{
		{"missing client id", with(func(c *AppCredentials) { c.ClientID = "" }), "", nil},
		{"missing installation", with(func(c *AppCredentials) { c.InstallationID = "" }), "", nil},
		{"API key as secret", with(func(c *AppCredentials) { c.ClientSecret = prodKey }), "", nil},
		{"unknown environment letter", with(func(c *AppCredentials) { c.ClientSecret = "cxq_cs_" + strings.Repeat("s", 43) }), "", nil},
		{"staging secret without WithBaseURL", staging, "", []Option{WithAuthURL(authURL)}},
		{"staging secret without WithBaseURL ignores host override", staging, apiURL, []Option{WithAuthURL(authURL)}},
		{"staging secret without WithAuthURL", staging, "", []Option{WithBaseURL(apiURL)}},
		{"non-http auth URL", creds, "", []Option{WithAuthURL("ftp://login.example.test")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(baseURLEnv, tc.env)
			c, err := NewApp(tc.creds, tc.opts...)
			if err == nil || c != nil {
				t.Fatalf("NewApp() = %v, %v; want an error", c, err)
			}
			if !strings.HasPrefix(err.Error(), "clickmax: ") {
				t.Errorf("error %q lacks the clickmax: prefix", err)
			}
		})
	}
}

func TestAppURLsFollowTheSecretEnvironment(t *testing.T) {
	cases := []struct {
		name         string
		secret       string
		opts         []Option
		wantToken    string
		wantResource string
		wantAPI      string
	}{
		{"production defaults", prodSecret, nil, tokenURL, "https://mcp.clickmax.io/api", DefaultBaseURL + "/whoami"},
		{"staging with explicit URLs", stagingSecret, []Option{
			WithBaseURL("https://MCP.example.test:443/api/v0/"),
			WithAuthURL("https://login.example.test/api/auth/"),
		}, "https://login.example.test/api/auth/oauth2/token", "https://mcp.example.test/api", "https://MCP.example.test:443/api/v0/whoami"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeServers{}
			c, err := NewApp(AppCredentials{ClientID: clientID, ClientSecret: tc.secret, InstallationID: installationID}, append(tc.opts, WithHTTPClient(f))...)
			if err != nil {
				t.Fatal(err)
			}
			if err := get(c); err != nil {
				t.Fatal(err)
			}
			if got := f.tokenReqs[0].URL.String(); got != tc.wantToken {
				t.Errorf("token URL = %q, want %q", got, tc.wantToken)
			}
			if got := f.tokenForms[0].Get("resource"); got != tc.wantResource {
				t.Errorf("resource = %q, want %q", got, tc.wantResource)
			}
			if got := f.apiReqs[0].URL.String(); got != tc.wantAPI {
				t.Errorf("API URL = %q, want %q", got, tc.wantAPI)
			}
		})
	}
}

func TestAppPerCallBaseURL(t *testing.T) {
	f := &fakeServers{}
	c := newTestApp(t, f)

	e := asError(t, get(c, WithBaseURL("https://other.example.test/api/v0")))
	if e.Type != ErrorTypeInvalidRequest || e.Code != "invalid_argument" {
		t.Errorf("other origin got %+v, want invalid_argument", e)
	}
	if tokens, bearers := f.counts(); tokens != 0 || len(bearers) != 0 {
		t.Fatalf("other origin sent %d token and %d API requests, want none", tokens, len(bearers))
	}

	if err := get(c, WithBaseURL("https://mcp.clickmax.io/api/v1")); err != nil {
		t.Fatalf("same origin: %v", err)
	}
	if got := f.apiReqs[0].URL.String(); got != "https://mcp.clickmax.io/api/v1/whoami" {
		t.Errorf("same origin sent to %q", got)
	}
}

func TestExchangeInstallCode(t *testing.T) {
	input := InstallCodeExchangeInput{
		ClientID:     clientID,
		ClientSecret: prodSecret,
		Code:         "code-1",
		CodeVerifier: "verifier-1",
		RedirectURI:  "https://app.example.test/callback",
	}
	cases := []struct {
		name    string
		input   InstallCodeExchangeInput
		status  int
		body    string
		want    *InstallCodeExchange
		wantErr Error
	}{
		{"install with openid", input, 200,
			`{"access_token":"opaque","id_token":"id.token.jwt","installation_id":"` + installationID + `","workspace_id":"workspace-1"}`,
			&InstallCodeExchange{InstallationID: installationID, WorkspaceID: "workspace-1", IDToken: "id.token.jwt"}, Error{}},
		{"sign-in without the install scope", input, 200, `{"access_token":"opaque","token_type":"Bearer"}`,
			nil, Error{Type: ErrorTypeInvalidRequest, Code: "not_an_install", Status: 200}},
		{"empty installation", input, 200, `{"access_token":"opaque","installation_id":"","workspace_id":"workspace-1"}`,
			nil, Error{Type: ErrorTypeParse, Code: "parse", Status: 200}},
		{"refused code", input, 400, `{"error":"invalid_grant","error_description":"code expired"}`,
			nil, Error{Type: ErrorTypeAuthentication, Code: "invalid_grant", Status: 400}},
		{"missing verifier", InstallCodeExchangeInput{ClientID: clientID, ClientSecret: prodSecret, Code: "code-1", RedirectURI: input.RedirectURI}, 0, "",
			nil, Error{Type: ErrorTypeInvalidRequest, Code: "invalid_argument"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeServers{token: func(int) (int, string) { return tc.status, tc.body }}
			got, err := ExchangeInstallCode(context.Background(), tc.input, WithHTTPClient(f))

			if tc.want != nil {
				if err != nil || !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
				}
				wantForm := url.Values{
					"grant_type":    {"authorization_code"},
					"code":          {"code-1"},
					"code_verifier": {"verifier-1"},
					"redirect_uri":  {"https://app.example.test/callback"},
				}
				if f.tokenReqs[0].URL.String() != tokenURL || !reflect.DeepEqual(f.tokenForms[0], wantForm) {
					t.Errorf("sent %s %v, want %s %v", f.tokenReqs[0].URL, f.tokenForms[0], tokenURL, wantForm)
				}
				return
			}
			e := asError(t, err)
			if gotErr := (Error{Type: e.Type, Code: e.Code, Status: e.Status}); got != nil || !reflect.DeepEqual(gotErr, tc.wantErr) {
				t.Errorf("got %+v, %+v; want error %+v", got, gotErr, tc.wantErr)
			}
			if tc.wantErr.Status == 200 && e.Raw != nil {
				t.Errorf("Raw = %q, want nil for a successful token response", e.Raw)
			}
			if tokens, _ := f.counts(); tc.status == 0 && tokens != 0 {
				t.Errorf("sent %d requests for invalid arguments, want none", tokens)
			}
		})
	}
}
