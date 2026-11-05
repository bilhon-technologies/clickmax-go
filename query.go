package clickmax

import (
	"net/url"
	"strconv"
	"time"
)

// The add* helpers build query strings in the generated values() methods of
// params structs. A nil pointer is a parameter the caller left out, so nothing
// is added.

func addString(v url.Values, key string, s *string) {
	if s != nil {
		v.Set(key, *s)
	}
}

func addInt(v url.Values, key string, n *int64) {
	if n != nil {
		v.Set(key, strconv.FormatInt(*n, 10))
	}
}

func addFloat(v url.Values, key string, f *float64) {
	if f != nil {
		v.Set(key, strconv.FormatFloat(*f, 'f', -1, 64))
	}
}

func addBool(v url.Values, key string, b *bool) {
	if b != nil {
		v.Set(key, strconv.FormatBool(*b))
	}
}

// addTime keeps the offset of t; the API accepts any RFC 3339 offset.
func addTime(v url.Values, key string, t *time.Time) {
	if t != nil {
		v.Set(key, t.Format(time.RFC3339Nano))
	}
}
