package clickmax

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

var (
	prodKey    = "cxp_" + strings.Repeat("a", 43)
	stagingKey = "cxs_" + strings.Repeat("b", 43)
)

// fakeAPI records every request and answers each one with the same response,
// so tests can inspect exactly what the client put on the wire.
type fakeAPI struct {
	reqs   []*http.Request
	status int
	header http.Header
	body   string
}

func (f *fakeAPI) Do(r *http.Request) (*http.Response, error) {
	f.reqs = append(f.reqs, r)
	status, header := f.status, f.header
	if status == 0 {
		status = http.StatusOK
	}
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(f.body)), Request: r}, nil
}

func get(c *Client, opts ...Option) error {
	return c.do(context.Background(), http.MethodGet, "/whoami", nil, nil, nil, opts)
}

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got error %v, want *Error", err)
	}
	return e
}

func TestNewResolvesBaseURL(t *testing.T) {
	const override = "https://override.example.test/v0/"
	cases := []struct {
		name string
		key  string
		env  string
		opts []Option
		want string
	}{
		{"production key ignores host override", prodKey, override, nil, DefaultBaseURL + "/whoami"},
		{"production key with WithBaseURL", prodKey, "", []Option{WithBaseURL("http://localhost:8080/api/v0//")}, "http://localhost:8080/api/v0/whoami"},
		{"non-production key uses host override", stagingKey, override, nil, "https://override.example.test/v0/whoami"},
		{"WithBaseURL wins over host override", stagingKey, override, []Option{WithBaseURL("https://option.example.test/v0")}, "https://option.example.test/v0/whoami"},
		{"empty WithBaseURL counts as absent", stagingKey, override, []Option{WithBaseURL("")}, "https://override.example.test/v0/whoami"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(baseURLEnv, tc.env)
			api := &fakeAPI{}
			c, err := New(tc.key, append(tc.opts, WithHTTPClient(api))...)
			if err != nil {
				t.Fatal(err)
			}
			if err := get(c); err != nil {
				t.Fatal(err)
			}
			if got := api.reqs[0].URL.String(); got != tc.want {
				t.Errorf("request URL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewRejects(t *testing.T) {
	cases := []struct {
		name string
		key  string
		opts []Option
	}{
		{"empty key", "", nil},
		{"short key", "cxp_short", nil},
		{"unknown environment letter", "cxq_" + strings.Repeat("a", 43), nil},
		{"non-production key without base URL", stagingKey, nil},
		{"non-http base URL", prodKey, []Option{WithBaseURL("ftp://example.test/v0")}},
		{"base URL without scheme", prodKey, []Option{WithBaseURL("example.test/v0")}},
		{"negative timeout", prodKey, []Option{WithTimeout(-time.Second)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(baseURLEnv, "")
			c, err := New(tc.key, tc.opts...)
			if err == nil || c != nil {
				t.Fatalf("New() = %v, %v; want an error", c, err)
			}
			if !strings.HasPrefix(err.Error(), "clickmax: ") {
				t.Errorf("error %q lacks the clickmax: prefix", err)
			}
		})
	}
}

func TestRequestHeaders(t *testing.T) {
	api := &fakeAPI{body: `{"id":"tag-1"}`}
	c, err := New(prodKey,
		WithHTTPClient(api),
		WithHeader("X-Tenant", "a"),
		WithHeader("X-Trace", "client"),
		WithHeader("Authorization", "Bearer client-override"),
	)
	if err != nil {
		t.Fatal(err)
	}

	var out map[string]any
	err = c.do(context.Background(), http.MethodPost, "/tags/create", nil, map[string]string{"name": "vip"}, &out, []Option{
		WithHeader("x-trace", "call"),
		WithHeader("authorization", "Bearer call-override"),
		WithHeader("X-Clickmax-Sdk", "other"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["id"] != "tag-1" {
		t.Errorf("decoded body = %v", out)
	}

	r := api.reqs[0]
	body, _ := io.ReadAll(r.Body)
	if r.Method != http.MethodPost || r.URL.String() != DefaultBaseURL+"/tags/create" || string(body) != `{"name":"vip"}` {
		t.Errorf("sent %s %s %s", r.Method, r.URL, body)
	}
	want := map[string]string{
		"Authorization":  "Bearer " + prodKey,
		"X-Clickmax-Sdk": "go/" + Version,
		"X-Trace":        "call",
		"X-Tenant":       "a",
		"Content-Type":   "application/json",
		"Accept":         "application/json",
		"User-Agent":     "clickmax-go/" + Version,
	}
	for name, value := range want {
		if got := r.Header.Values(name); len(got) != 1 || got[0] != value {
			t.Errorf("header %s = %q, want %q", name, got, value)
		}
	}
}

func TestPerCallOptionsApplyToOneCall(t *testing.T) {
	clientAPI, callAPI := &fakeAPI{}, &fakeAPI{}
	c, err := New(prodKey, WithHTTPClient(clientAPI), WithHeader("X-Trace", "client"))
	if err != nil {
		t.Fatal(err)
	}

	if err := get(c,
		WithHTTPClient(callAPI),
		WithBaseURL("https://other.example.test/v0/"),
		WithHeader("X-Trace", "call"),
		WithHeader("X-Extra", "1"),
	); err != nil {
		t.Fatal(err)
	}
	if err := get(c, WithTimeout(-1)); err == nil {
		t.Fatal("negative per-call timeout accepted")
	}
	if err := get(c); err != nil {
		t.Fatal(err)
	}

	if len(callAPI.reqs) != 1 || callAPI.reqs[0].URL.String() != "https://other.example.test/v0/whoami" || callAPI.reqs[0].Header.Get("X-Trace") != "call" {
		t.Errorf("per-call request not sent as configured: %+v", callAPI.reqs)
	}
	if len(clientAPI.reqs) != 1 {
		t.Fatalf("client HTTPClient got %d requests, want 1", len(clientAPI.reqs))
	}
	r := clientAPI.reqs[0]
	if r.URL.String() != DefaultBaseURL+"/whoami" || r.Header.Get("X-Trace") != "client" || r.Header.Get("X-Extra") != "" {
		t.Errorf("per-call options leaked into the client: %s %v", r.URL, r.Header)
	}
}
