package clickmax

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrorType is the category of an [Error]. The API reports the categories from
// [ErrorTypeInvalidRequest] to [ErrorTypeAPI]; the SDK derives the same one
// from the HTTP status when the response has no JSON body, such as an HTML
// page from a proxy on a 502. The SDK itself produces [ErrorTypeNetwork],
// [ErrorTypeTimeout], [ErrorTypeAborted] and [ErrorTypeParse].
type ErrorType string

const (
	// ErrorTypeInvalidRequest means the request was rejected as sent: invalid
	// input (code "invalid_input", see [Error.Issues]) or an argument the SDK
	// refused to send (code "invalid_argument"). Fix the call before retrying.
	ErrorTypeInvalidRequest ErrorType = "invalid_request"
	// ErrorTypeAuthentication means the API key is missing, revoked or invalid
	// (HTTP 401).
	ErrorTypeAuthentication ErrorType = "authentication"
	// ErrorTypePlanLimit means the workspace plan does not allow the operation
	// or a quota is used up (HTTP 402). [Error.Metric] names the quota.
	ErrorTypePlanLimit ErrorType = "plan_limit"
	// ErrorTypePermission means the key lacks a permission (HTTP 403).
	// [Error.Capability] names it.
	ErrorTypePermission ErrorType = "permission"
	// ErrorTypeNotFound means the resource does not exist or is not visible to
	// the key (HTTP 404 or 410).
	ErrorTypeNotFound ErrorType = "not_found"
	// ErrorTypeConflict means the request clashes with the current state of
	// the resource (HTTP 409 or 412).
	ErrorTypeConflict ErrorType = "conflict"
	// ErrorTypeRateLimit means too many requests were sent (HTTP 429). Wait
	// [Error.RetryAfter] before retrying.
	ErrorTypeRateLimit ErrorType = "rate_limit"
	// ErrorTypeAPI means the API or a service behind it failed (HTTP 5xx or
	// 408). The request may succeed if retried later.
	ErrorTypeAPI ErrorType = "api"
	// ErrorTypeNetwork means no response arrived: DNS failure, refused or
	// reset connection, TLS error.
	ErrorTypeNetwork ErrorType = "network"
	// ErrorTypeTimeout means the request ran past [WithTimeout] or the
	// deadline of its context.
	ErrorTypeTimeout ErrorType = "timeout"
	// ErrorTypeAborted means the context of the call was canceled.
	ErrorTypeAborted ErrorType = "aborted"
	// ErrorTypeParse means a 2xx response carried a body that could not be
	// decoded. [Error.Raw] holds it.
	ErrorTypeParse ErrorType = "parse"
)

// Error is the error returned by every failed method call. Use [errors.As] to
// reach it:
//
//	var cerr *clickmax.Error
//	if errors.As(err, &cerr) && cerr.Type == clickmax.ErrorTypeRateLimit {
//		time.Sleep(cerr.RetryAfter)
//	}
//
// Branch on Type for control flow and on Code for specific cases. Message is
// meant for logs and may change between releases.
type Error struct {
	// Type is the error category.
	Type ErrorType
	// Code is a stable lower_snake identifier of the specific failure, such as
	// "unauthorized", "invalid_input" or "plan_limit_reached". It equals Type
	// when the response did not carry one.
	Code string
	// Status is the HTTP status of the response, or 0 when none was received.
	Status int
	// Message describes the failure for humans.
	Message string
	// RequestID identifies the request on the Clickmax side. Include it when
	// contacting support.
	RequestID string
	// RetryAfter is how long to wait before retrying, read from the
	// Retry-After header. It is 0 when the header is absent or unreadable.
	RetryAfter time.Duration
	// Issues lists the invalid fields of an "invalid_input" error.
	Issues []Issue
	// Capability is the permission the key lacks, set when Code is
	// "missing_permission".
	Capability string
	// Metric is the plan quota that was reached, set when Code is
	// "plan_limit_reached".
	Metric string
	// UpstreamStatus is the status a service behind the API answered with,
	// set when Code is "upstream_error".
	UpstreamStatus int
	// Raw is the response body as received, or nil when there was none.
	Raw []byte
	// Err is the underlying cause, such as a network error or
	// [context.Canceled]. [errors.Is] and [errors.As] see it through Unwrap.
	Err error
}

// Issue is one validation problem of an "invalid_input" error.
type Issue struct {
	// Code identifies the kind of problem, such as "too_small".
	Code string `json:"code"`
	// Message describes the problem for humans.
	Message string `json:"message"`
	// Path locates the invalid value in the request: string elements are
	// object keys and int elements are array indexes.
	Path []any `json:"path"`
}

// Error formats the error as
// "clickmax: <message> (type=<t>, code=<c>, status=<n>, request_id=<id>)",
// leaving out the parts that are empty.
func (e *Error) Error() string {
	var parts []string
	if e.Type != "" {
		parts = append(parts, "type="+string(e.Type))
	}
	if e.Code != "" {
		parts = append(parts, "code="+e.Code)
	}
	if e.Status != 0 {
		parts = append(parts, "status="+strconv.Itoa(e.Status))
	}
	if e.RequestID != "" {
		parts = append(parts, "request_id="+e.RequestID)
	}
	s := "clickmax: " + e.Message
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s
}

// Unwrap returns Err.
func (e *Error) Unwrap() error {
	return e.Err
}

// Plain text bodies longer than this are almost always HTML or stack traces,
// not a message.
const maxTextMessageLength = 300

// errorFromResponse builds the error for a non-2xx response. The body's type
// and code win when present; the status mapping covers bodies produced in
// front of the API, such as text or HTML pages from the edge.
func errorFromResponse(status int, header http.Header, data []byte) *Error {
	// Decoded into raw fields, not a struct, because struct decoding matches
	// keys case-insensitively and would read "TYPE" or "Error" from a body
	// the API did not write. A body that is not a JSON object leaves body nil,
	// and each field of the wrong type reads as absent.
	var body map[string]json.RawMessage
	_ = json.Unmarshal(data, &body)

	typ := ErrorType(stringField(body, "type"))
	if !isAPIErrorType(typ) {
		typ = typeFromStatus(status)
	}
	e := &Error{
		Type:           typ,
		Code:           firstNonEmpty(stringField(body, "code"), string(typ)),
		Status:         status,
		Message:        firstNonEmpty(stringField(body, "error"), stringField(body, "message"), textMessage(header, data), fmt.Sprintf("Request failed with status %d", status)),
		RequestID:      firstNonEmpty(stringField(body, "requestId"), header.Get("Cf-Ray")),
		RetryAfter:     parseRetryAfter(header.Get("Retry-After"), time.Now()),
		Issues:         issuesField(body),
		Capability:     stringField(body, "capability"),
		Metric:         stringField(body, "metric"),
		UpstreamStatus: intField(body, "upstreamStatus"),
	}
	if len(data) > 0 {
		e.Raw = data
	}
	return e
}

func stringField(fields map[string]json.RawMessage, key string) string {
	var s string
	if json.Unmarshal(fields[key], &s) != nil {
		return ""
	}
	return s
}

// intField accepts any whole JSON number, such as 503.0, which JSON.parse
// reads as the integer 503 but encoding/json refuses to decode into an int.
func intField(fields map[string]json.RawMessage, key string) int {
	var f float64
	if json.Unmarshal(fields[key], &f) != nil || f != math.Trunc(f) || math.Abs(f) >= 1<<53 {
		return 0
	}
	return int(f)
}

// issuesField skips entries that are not JSON objects, which would otherwise
// show up as empty issues.
func issuesField(fields map[string]json.RawMessage) []Issue {
	var entries []json.RawMessage
	if json.Unmarshal(fields["issues"], &entries) != nil {
		return nil
	}
	var issues []Issue
	for _, raw := range entries {
		var entry map[string]json.RawMessage
		// A JSON null decodes into a nil map without an error.
		if json.Unmarshal(raw, &entry) != nil || entry == nil {
			continue
		}
		var path []any
		_ = json.Unmarshal(entry["path"], &path)
		normalizePath(path)
		issues = append(issues, Issue{
			Code:    stringField(entry, "code"),
			Message: stringField(entry, "message"),
			Path:    path,
		})
	}
	return issues
}

// normalizePath turns the float64 array indexes produced by encoding/json into
// ints, so callers can type-switch on string keys and int indexes.
func normalizePath(path []any) {
	for i, p := range path {
		if f, ok := p.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			path[i] = int(f)
		}
	}
}

func isAPIErrorType(t ErrorType) bool {
	switch t {
	case ErrorTypeInvalidRequest, ErrorTypeAuthentication, ErrorTypePlanLimit, ErrorTypePermission,
		ErrorTypeNotFound, ErrorTypeConflict, ErrorTypeRateLimit, ErrorTypeAPI:
		return true
	}
	return false
}

// typeFromStatus mirrors the API's own mapping, so Type is the same whether or
// not the body could be read.
func typeFromStatus(status int) ErrorType {
	switch status {
	case 401:
		return ErrorTypeAuthentication
	case 402:
		return ErrorTypePlanLimit
	case 403:
		return ErrorTypePermission
	case 404, 410:
		return ErrorTypeNotFound
	// The API sends 408 only when one of its own upstream calls timed out, so
	// the caller can only retry, as with a 5xx.
	case 408:
		return ErrorTypeAPI
	case 409, 412:
		return ErrorTypeConflict
	case 429:
		return ErrorTypeRateLimit
	}
	if status >= 500 {
		return ErrorTypeAPI
	}
	return ErrorTypeInvalidRequest
}

// parseRetryAfter accepts both forms of RFC 9110: delay seconds or an HTTP
// date. A date in the past, an unreadable value or a delay too large for a
// Duration yields 0. Dates round up to whole seconds.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if isDigits(value) {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds > int64(math.MaxInt64/time.Second) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	at, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	d := at.Sub(now)
	if d <= 0 {
		return 0
	}
	if rem := d % time.Second; rem != 0 {
		d += time.Second - rem
	}
	return d
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// textMessage uses a short text/plain body as the message, the form edge
// proxies answer with. HTML and long bodies stay in Raw only.
func textMessage(header http.Header, data []byte) string {
	if !strings.HasPrefix(strings.ToLower(header.Get("Content-Type")), "text/plain") {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if utf8.RuneCountInString(text) > maxTextMessageLength {
		return ""
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
