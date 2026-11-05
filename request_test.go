package clickmax

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

// stallingServer answers nothing until the request's context ends. With
// sendHeaders it first flushes a 200 status, so the stall happens while the
// client reads the body. started fires once the handler runs.
func stallingServer(t *testing.T, sendHeaders bool) (srv *httptest.Server, started <-chan struct{}) {
	ch := make(chan struct{}, 1)
	release := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sendHeaders {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		select {
		case ch <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// Cleanups run last-in first-out: release stalled handlers before Close
	// waits for them.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv, ch
}

func cancelOnStart(started <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func TestTransportFailures(t *testing.T) {
	background := func(<-chan struct{}) (context.Context, context.CancelFunc) {
		return context.WithCancel(context.Background())
	}
	cases := []struct {
		name        string
		sendHeaders bool
		timeout     time.Duration
		ctx         func(started <-chan struct{}) (context.Context, context.CancelFunc)
		wantType    ErrorType
		wantIs      error
	}{
		{"SDK timeout before the headers", false, 20 * time.Millisecond, background, ErrorTypeTimeout, context.DeadlineExceeded},
		{"SDK timeout while reading the body", true, 20 * time.Millisecond, background, ErrorTypeTimeout, context.DeadlineExceeded},
		{"caller deadline", false, 0, func(<-chan struct{}) (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}, ErrorTypeTimeout, context.DeadlineExceeded},
		{"caller cancel before the headers", false, time.Minute, cancelOnStart, ErrorTypeAborted, context.Canceled},
		{"caller cancel while reading the body", true, time.Minute, cancelOnStart, ErrorTypeAborted, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, started := stallingServer(t, tc.sendHeaders)
			c, err := New(prodKey, WithBaseURL(srv.URL), WithTimeout(tc.timeout))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := tc.ctx(started)
			defer cancel()

			err = c.do(ctx, http.MethodGet, "/whoami", nil, nil, nil, nil)
			e := asError(t, err)
			if e.Type != tc.wantType || e.Status != 0 {
				t.Errorf("got type %q status %d, want %q status 0 (%v)", e.Type, e.Status, tc.wantType, err)
			}
			if !errors.Is(err, tc.wantIs) {
				t.Errorf("errors.Is(%v, %v) = false", err, tc.wantIs)
			}
		})
	}

	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		c, err := New(prodKey, WithBaseURL(srv.URL))
		if err != nil {
			t.Fatal(err)
		}
		if e := asError(t, get(c)); e.Type != ErrorTypeNetwork || e.Err == nil {
			t.Errorf("got %+v, want a network error with its cause", e)
		}
	})
}

func TestInvalidArgumentsAreNotSent(t *testing.T) {
	cases := []struct {
		name string
		body any
		opts []Option
	}{
		{"negative per-call timeout", nil, []Option{WithTimeout(-1)}},
		{"invalid per-call base URL", nil, []Option{WithBaseURL("ftp://example.test")}},
		{"body without a JSON form", map[string]any{"name": func() {}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			c, err := New(prodKey, WithHTTPClient(api))
			if err != nil {
				t.Fatal(err)
			}
			err = c.do(context.Background(), http.MethodPost, "/tags/create", nil, tc.body, nil, tc.opts)
			e := asError(t, err)
			if e.Type != ErrorTypeInvalidRequest || e.Code != "invalid_argument" || e.Status != 0 {
				t.Errorf("got %+v, want invalid_request/invalid_argument with status 0", e)
			}
			if len(api.reqs) != 0 {
				t.Errorf("sent %d requests, want none", len(api.reqs))
			}
		})
	}
}

func TestSuccessBody(t *testing.T) {
	t.Run("undecodable", func(t *testing.T) {
		c, err := New(prodKey, WithHTTPClient(&fakeAPI{body: "{", header: http.Header{"Cf-Ray": {"ray-1"}}}))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		e := asError(t, c.do(context.Background(), http.MethodGet, "/whoami", nil, nil, &out, nil))
		if e.Type != ErrorTypeParse || e.Status != 200 || string(e.Raw) != "{" || e.RequestID != "ray-1" {
			t.Errorf("got %+v, want a parse error carrying the body", e)
		}
	})

	t.Run("empty", func(t *testing.T) {
		c, err := New(prodKey, WithHTTPClient(&fakeAPI{}))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]any{"kept": true}
		if err := c.do(context.Background(), http.MethodGet, "/whoami", nil, nil, &out, nil); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(out, map[string]any{"kept": true}) {
			t.Errorf("out = %v, want it untouched", out)
		}
	})
}

func TestQueryEncoding(t *testing.T) {
	v := url.Values{}
	addString(v, "search", Ptr("a b&c=d+é"))
	addString(v, "status", nil)
	addInt(v, "page", Ptr[int64](2))
	addInt(v, "perPage", nil)
	addFloat(v, "minScore", Ptr(0.5))
	addFloat(v, "big", Ptr(1e21))
	addBool(v, "archived", Ptr(false))
	addTime(v, "from", Ptr(time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("BRT", -3*3600))))

	api := &fakeAPI{}
	c, err := New(prodKey, WithHTTPClient(api))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.do(context.Background(), http.MethodGet, "/leads/list", v, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	want := url.Values{
		"search":   {"a b&c=d+é"},
		"page":     {"2"},
		"minScore": {"0.5"},
		"big":      {"1000000000000000000000"},
		"archived": {"false"},
		"from":     {"2026-01-02T03:04:05-03:00"},
	}
	r := api.reqs[0]
	if r.URL.Path != "/api/v0/leads/list" {
		t.Errorf("path = %q", r.URL.Path)
	}
	if got := r.URL.Query(); !reflect.DeepEqual(got, want) {
		t.Errorf("query = %v, want %v", got, want)
	}
}
