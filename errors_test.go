package clickmax

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestErrorFromResponse(t *testing.T) {
	jsonType := http.Header{"Content-Type": {"application/json"}}
	cases := []struct {
		name   string
		status int
		header http.Header
		body   string
		want   Error
	}{
		{
			name:   "body type wins over the status",
			status: 400,
			header: jsonType,
			body:   `{"error":"Lead limit reached","type":"plan_limit","code":"plan_limit_reached","metric":"leads","requestId":"req-1"}`,
			want:   Error{Type: ErrorTypePlanLimit, Code: "plan_limit_reached", Status: 400, Message: "Lead limit reached", RequestID: "req-1", Metric: "leads"},
		},
		{
			name:   "body requestId wins over cf-ray",
			status: 403,
			header: http.Header{"Content-Type": {"application/json"}, "Cf-Ray": {"ray-ignored"}},
			body:   `{"error":"Missing permission","type":"permission","code":"missing_permission","capability":"tags.write","requestId":"req-2"}`,
			want:   Error{Type: ErrorTypePermission, Code: "missing_permission", Status: 403, Message: "Missing permission", RequestID: "req-2", Capability: "tags.write"},
		},
		{
			name:   "validation issues",
			status: 400,
			header: jsonType,
			body:   `{"error":"Invalid input","type":"invalid_request","code":"invalid_input","issues":[{"code":"too_small","message":"Too short","path":["items",0,"name"]}]}`,
			want: Error{Type: ErrorTypeInvalidRequest, Code: "invalid_input", Status: 400, Message: "Invalid input",
				Issues: []Issue{{Code: "too_small", Message: "Too short", Path: []any{"items", 0, "name"}}}},
		},
		{
			name:   "upstream status",
			status: 502,
			header: jsonType,
			body:   `{"error":"Upstream failed","type":"api","code":"upstream_error","upstreamStatus":503}`,
			want:   Error{Type: ErrorTypeAPI, Code: "upstream_error", Status: 502, Message: "Upstream failed", UpstreamStatus: 503},
		},
		{
			name:   "JSON body without type falls back to the status",
			status: 401,
			header: jsonType,
			body:   `{"error":"Unauthorized"}`,
			want:   Error{Type: ErrorTypeAuthentication, Code: "authentication", Status: 401, Message: "Unauthorized"},
		},
		{
			name:   "unknown body type falls back to the status and message field is read",
			status: 404,
			header: jsonType,
			body:   `{"type":"gone_forever","message":"Tag not found"}`,
			want:   Error{Type: ErrorTypeNotFound, Code: "not_found", Status: 404, Message: "Tag not found"},
		},
		{
			name:   "plain text 429 with Retry-After and cf-ray",
			status: 429,
			header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}, "Retry-After": {"10"}, "Cf-Ray": {"ray-1"}},
			body:   "Too many requests\n",
			want:   Error{Type: ErrorTypeRateLimit, Code: "rate_limit", Status: 429, Message: "Too many requests", RequestID: "ray-1", RetryAfter: 10 * time.Second},
		},
		{
			name:   "HTML 502 from the edge",
			status: 502,
			header: http.Header{"Content-Type": {"text/html"}},
			body:   "<html><body>Bad gateway</body></html>",
			want:   Error{Type: ErrorTypeAPI, Code: "api", Status: 502, Message: "Request failed with status 502"},
		},
		{
			name:   "long plain text body is not a message",
			status: 503,
			header: http.Header{"Content-Type": {"text/plain"}},
			body:   strings.Repeat("x", 301),
			want:   Error{Type: ErrorTypeAPI, Code: "api", Status: 503, Message: "Request failed with status 503"},
		},
		{
			name:   "empty 408 is retryable like a 5xx",
			status: 408,
			want:   Error{Type: ErrorTypeAPI, Code: "api", Status: 408, Message: "Request failed with status 408"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(prodKey, WithHTTPClient(&fakeAPI{status: tc.status, header: tc.header, body: tc.body}))
			if err != nil {
				t.Fatal(err)
			}
			got := *asError(t, get(c))
			if string(got.Raw) != tc.body {
				t.Errorf("Raw = %q, want the body %q", got.Raw, tc.body)
			}
			got.Raw, got.Err = nil, nil
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, int(500*time.Millisecond), time.UTC)
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"120", 120 * time.Second},
		{" 5 ", 5 * time.Second},
		{"0", 0},
		{"Fri, 02 Jan 2026 03:05:00 GMT", 55 * time.Second}, // 54.5s rounds up
		{"Fri, 02 Jan 2026 03:00:00 GMT", 0},                // in the past
		{"-5", 0},
		{"1.5", 0},
		{"soon", 0},
		{"99999999999999999999", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.value, now); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}
