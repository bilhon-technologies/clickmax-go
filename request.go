package clickmax

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// do sends one API request and decodes a 2xx JSON body into out, a pointer.
// Every generated method is a thin wrapper around it.
//
// path is the operation path from the spec, such as "/tags/list". A nil body
// sends no body. The returned error is nil or an *Error.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any, opts []Option) error {
	cfg := c.cfg.with(opts)
	// The client's base URL was checked in New or NewApp; only a per-call one
	// needs it.
	if cfg.baseURL != c.cfg.baseURL {
		baseURL, err := normalizeBaseURL(cfg.baseURL)
		if err != nil {
			return invalidArgument(err)
		}
		// The installation token would be refused by an API on another origin.
		if cfg.app != nil {
			resource, err := apiResource(baseURL)
			if err != nil {
				return invalidArgument(err)
			}
			if resource != cfg.app.resource {
				return invalidArgument(fmt.Errorf("base URL %q is not on the API the client's installation tokens are issued for (%s); build another client with NewApp", cfg.baseURL, cfg.app.resource))
			}
		}
		cfg.baseURL = baseURL
	}
	if cfg.timeout < 0 {
		return invalidArgument(errNegativeTimeout)
	}

	var reqBody []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return invalidArgument(fmt.Errorf("encode request body: %w", err))
		}
		reqBody = data
	}

	// One deadline for the whole call: waiting for a token, the request and
	// the retry after a 401.
	reqCtx, cancel := ctx, context.CancelFunc(func() {})
	if cfg.timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, cfg.timeout)
	}
	defer cancel()

	target := cfg.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	token := cfg.apiKey
	if cfg.app != nil {
		var err error
		if token, err = cfg.app.get(reqCtx); err != nil {
			return tokenWaitError(ctx, reqCtx, cfg.timeout, err)
		}
	}
	resp, data, cerr := send(ctx, reqCtx, cfg, method, target, reqBody, token)
	if cerr != nil {
		return cerr
	}
	// A cached installation token can stop working before its expiry (clock
	// skew, a rotated client secret), so one 401 gets one retry with a fresh
	// token. An API key cannot be replaced, so key clients never retry.
	if resp.StatusCode == http.StatusUnauthorized && cfg.app != nil {
		var err error
		if token, err = cfg.app.renew(reqCtx, token); err != nil {
			return tokenWaitError(ctx, reqCtx, cfg.timeout, err)
		}
		if resp, data, cerr = send(ctx, reqCtx, cfg, method, target, reqBody, token); cerr != nil {
			return cerr
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errorFromResponse(resp.StatusCode, resp.Header, data)
	}
	// 2xx bodies follow the published spec and are not validated beyond what
	// decoding into the generated types checks.
	if len(data) == 0 || out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{
			Type:      ErrorTypeParse,
			Code:      string(ErrorTypeParse),
			Status:    resp.StatusCode,
			Message:   fmt.Sprintf("decode response with status %d: %v", resp.StatusCode, err),
			RequestID: resp.Header.Get("Cf-Ray"),
			Raw:       data,
			Err:       err,
		}
	}
	return nil
}

// send makes one attempt of a call with the bearer token and returns the
// response with its body read. body is sent again as is on a retry.
func send(ctx, reqCtx context.Context, cfg settings, method, target string, body []byte, token string) (*http.Response, []byte, *Error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, target, reqBody)
	if err != nil {
		return nil, nil, invalidArgument(err)
	}
	if cfg.header != nil {
		req.Header = cfg.header.Clone()
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Set after the client and per-call headers so neither can replace the
	// credentials or the SDK version.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Clickmax-Sdk", "go/"+Version)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	// Without it net/http sends its own "Go-http-client" agent.
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "clickmax-go/"+Version)
	}

	client := cfg.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, transportError(ctx, reqCtx, cfg.timeout, err)
	}
	var data []byte
	if resp.Body != nil {
		// Read before cancel runs, so the timeout and ctx also cut a body that
		// stalls after the headers arrived.
		data, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, nil, transportError(ctx, reqCtx, cfg.timeout, err)
		}
	}
	return resp, data, nil
}

// tokenWaitError returns token request failures as they are; they were
// classified when the request failed. Anything else is reqCtx ending while
// the call waited.
func tokenWaitError(ctx, reqCtx context.Context, timeout time.Duration, err error) error {
	var cerr *Error
	if errors.As(err, &cerr) {
		return cerr
	}
	return transportError(ctx, reqCtx, timeout, err)
}

// invalidArgument reports a call the SDK refused to send.
func invalidArgument(err error) *Error {
	return &Error{
		Type:    ErrorTypeInvalidRequest,
		Code:    "invalid_argument",
		Message: err.Error(),
		Err:     err,
	}
}

// transportError classifies a failed send or body read. The caller's ctx is
// checked first so that a cancel racing the SDK timeout reports aborted, and a
// deadline the caller set reports timeout like the SDK's own.
func transportError(ctx, reqCtx context.Context, timeout time.Duration, err error) *Error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return &Error{
				Type:    ErrorTypeTimeout,
				Code:    string(ErrorTypeTimeout),
				Message: "request exceeded the context deadline",
				Err:     withCause(err, ctxErr),
			}
		}
		return &Error{
			Type:    ErrorTypeAborted,
			Code:    string(ErrorTypeAborted),
			Message: "request canceled",
			Err:     withCause(err, ctxErr),
		}
	}
	if ctxErr := reqCtx.Err(); ctxErr != nil {
		return &Error{
			Type:    ErrorTypeTimeout,
			Code:    string(ErrorTypeTimeout),
			Message: fmt.Sprintf("request timed out after %s", timeout),
			Err:     withCause(err, ctxErr),
		}
	}
	return &Error{
		Type:    ErrorTypeNetwork,
		Code:    string(ErrorTypeNetwork),
		Message: "network request failed: " + err.Error(),
		Err:     err,
	}
}

// withCause makes errors.Is(err, ctxErr) hold even when a custom HTTPClient
// returns an error that does not wrap the context error.
func withCause(err, ctxErr error) error {
	if errors.Is(err, ctxErr) {
		return err
	}
	return fmt.Errorf("%w: %w", ctxErr, err)
}
