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

// errorBody is the JSON error body of the API. Every field is optional here
// because proxies in front of the API answer with other shapes.
type errorBody struct {
	Error          string  `json:"error"`
	Message        string  `json:"message"`
	Type           string  `json:"type"`
	Code           string  `json:"code"`
	RequestID      string  `json:"requestId"`
	Issues         []Issue `json:"issues"`
	Capability     string  `json:"capability"`
	Metric         string  `json:"metric"`
	UpstreamStatus int     `json:"upstreamStatus"`
}

// Plain text bodies longer than this are almost always HTML or stack traces,
// not a message.
const maxTextMessageLength = 300

// errorFromResponse builds the error for a non-2xx response. The body's type
// and code win when present; the status mapping covers bodies produced in
// front of the API, such as text or HTML pages from the edge.
func errorFromResponse(status int, header http.Header, data []byte) *Error {
	var body errorBody
	// A body that is not a JSON object leaves body empty. A field of the wrong
	// type is skipped while the others still decode, so the error is ignored.
	_ = json.Unmarshal(data, &body)

	typ := ErrorType(body.Type)
	if !isAPIErrorType(typ) {
		typ = typeFromStatus(status)
	}
	e := &Error{
		Type:           typ,
		Code:           firstNonEmpty(body.Code, string(typ)),
		Status:         status,
		Message:        firstNonEmpty(body.Error, body.Message, textMessage(header, data), fmt.Sprintf("Request failed with status %d", status)),
		RequestID:      firstNonEmpty(body.RequestID, header.Get("Cf-Ray")),
		RetryAfter:     parseRetryAfter(header.Get("Retry-After"), time.Now()),
		Issues:         body.Issues,
		Capability:     body.Capability,
		Metric:         body.Metric,
		UpstreamStatus: body.UpstreamStatus,
	}
	if len(data) > 0 {
		e.Raw = data
	}
	for i := range e.Issues {
		normalizePath(e.Issues[i].Path)
	}
	return e
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
