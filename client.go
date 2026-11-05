package clickmax

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the production API. Clients use it unless [WithBaseURL]
// gives another one.
const DefaultBaseURL = "https://mcp.clickmax.io/api/v0"

const defaultTimeout = 60 * time.Second

const baseURLEnv = "CLICKMAX_CLIENT_BASE_URL"

// The letter after "cx" is the environment the key was issued in. Valid
// letters are the keys of keyEnvironments, so the pattern accepts any letter.
var apiKeyPattern = regexp.MustCompile(`^cx([a-z])_[A-Za-z0-9_-]{43}$`)

// Production ("p") maps to "" because its keys work against DefaultBaseURL.
var keyEnvironments = map[string]string{
	"p": "",
	"d": "development",
	"s": "staging",
	"t": "test",
}

var errNegativeTimeout = errors.New("timeout must not be negative")

// New returns a client that authenticates every request with apiKey, a
// workspace API key ("cxp_" followed by 43 characters).
//
// New makes no request. It returns an error when the key is empty or
// malformed, when an option is invalid, or when a key issued for another
// environment ("cxd_", "cxs_", "cxt_") has no [WithBaseURL]: the production API
// would reject such a key with a plain authentication error.
func New(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("clickmax: missing API key")
	}
	var prefix, env string
	known := false
	if m := apiKeyPattern.FindStringSubmatch(apiKey); m != nil {
		prefix = m[1]
		env, known = keyEnvironments[prefix]
	}
	if !known {
		return nil, errors.New(`clickmax: malformed API key; expected "cxp_" followed by 43 characters`)
	}

	cfg := settings{apiKey: apiKey, header: http.Header{}, timeout: defaultTimeout}.with(opts)
	if cfg.baseURL == "" && env != "" {
		// Lets a non-production key find its API without code changes. Production
		// keys never read it, so a variable set on the host cannot send them
		// anywhere but DefaultBaseURL. Kept out of the godoc on purpose.
		cfg.baseURL = os.Getenv(baseURLEnv)
	}
	if cfg.baseURL == "" {
		if env != "" {
			return nil, fmt.Errorf(`clickmax: "cx%s_" keys belong to the %s environment and do not work against %s; pass the matching base URL with WithBaseURL`, prefix, env, DefaultBaseURL)
		}
		cfg.baseURL = DefaultBaseURL
	}

	baseURL, err := normalizeBaseURL(cfg.baseURL)
	if err != nil {
		return nil, fmt.Errorf("clickmax: %w", err)
	}
	cfg.baseURL = baseURL
	if cfg.timeout < 0 {
		return nil, fmt.Errorf("clickmax: %w", errNegativeTimeout)
	}
	return newClient(cfg), nil
}

// normalizeBaseURL trims trailing slashes so operation paths, which start with
// "/", can be appended as they are.
func normalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(raw, "/")
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("base URL must be an http or https URL, got %q", raw)
	}
	return trimmed, nil
}
