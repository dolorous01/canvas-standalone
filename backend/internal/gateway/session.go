package gateway

import (
	"context"
	"net/http"
	"strings"
)

type sessionHeadersKey struct{}

// WithSessionHeaders preserves the fingerprint supplied by the trusted path
// proxy. The API must remain private, and official calls must use the internal
// proxy URL: a public CDN would replace the original client address.
func WithSessionHeaders(ctx context.Context, headers http.Header) context.Context {
	copy := make(http.Header)
	for _, name := range []string{"User-Agent", "CF-Connecting-IP", "X-Real-IP", "X-Forwarded-For"} {
		copy.Set(name, headers.Get(name))
	}
	return context.WithValue(ctx, sessionHeadersKey{}, copy)
}

func SessionFingerprint(ctx context.Context) string {
	headers, _ := ctx.Value(sessionHeadersKey{}).(http.Header)
	var values []string
	for _, name := range []string{"User-Agent", "CF-Connecting-IP", "X-Real-IP", "X-Forwarded-For"} {
		values = append(values, headers.Get(name))
	}
	return strings.Join(values, "\x00")
}

func forwardSessionHeaders(ctx context.Context, request *http.Request) {
	headers, _ := ctx.Value(sessionHeadersKey{}).(http.Header)
	for name, values := range headers {
		request.Header[name] = append([]string(nil), values...)
	}
}
