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
)

// do sends one API request and decodes a 2xx JSON body into out, a pointer.
// Every generated method is a thin wrapper around it.
//
// path is the operation path from the spec, such as "/tags/list". A nil body
// sends no body. The returned error is nil or an *Error.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any, opts []Option) error {
	cfg := c.cfg.with(opts)
	// The client's base URL was checked in New; only a per-call one needs it.
	if cfg.baseURL != c.cfg.baseURL {
		baseURL, err := normalizeBaseURL(cfg.baseURL)
		if err != nil {
			return invalidArgument(err)
		}
		cfg.baseURL = baseURL
	}
	if cfg.timeout < 0 {
		return invalidArgument(errNegativeTimeout)
	}

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return invalidArgument(fmt.Errorf("encode request body: %w", err))
		}
		reqBody = bytes.NewReader(data)
	}

	reqCtx, cancel := ctx, context.CancelFunc(func() {})
	if cfg.timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, cfg.timeout)
	}
	defer cancel()

	target := cfg.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(reqCtx, method, target, reqBody)
	if err != nil {
		return invalidArgument(err)
	}
	if cfg.header != nil {
		req.Header = cfg.header.Clone()
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Set after the client and per-call headers so neither can replace the key
	// or the SDK version.
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
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
		return transportError(ctx, reqCtx, cfg, err)
	}
	var data []byte
	if resp.Body != nil {
		// Read before cancel runs, so the timeout and ctx also cut a body that
		// stalls after the headers arrived.
		data, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return transportError(ctx, reqCtx, cfg, err)
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
func transportError(ctx, reqCtx context.Context, cfg settings, err error) *Error {
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
			Message: fmt.Sprintf("request timed out after %s", cfg.timeout),
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
