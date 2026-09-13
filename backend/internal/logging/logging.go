package logging

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

var (
	secretKeyPattern = regexp.MustCompile(`(?i)(authorization|bearer|token|secret|password|api.?key|database.?url|dsn|ciphertext|nonce)`)
	bearerPattern    = regexp.MustCompile(`(?i)bearer[[:space:]]+[A-Za-z0-9._~+/=-]{4,}`)
	apiKeyPattern    = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
	dsnPattern       = regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:/[:space:]]+):[^@[:space:]]+@`)
)

func New(writer io.Writer, level slog.Leveler) *slog.Logger {
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
			if secretKeyPattern.MatchString(attribute.Key) {
				attribute.Value = slog.StringValue("[REDACTED]")
				return attribute
			}
			if attribute.Value.Kind() == slog.KindString {
				attribute.Value = slog.StringValue(Redact(attribute.Value.String()))
			}
			return attribute
		},
	})
	return slog.New(handler)
}

func Redact(value string) string {
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = apiKeyPattern.ReplaceAllString(value, "sk-[REDACTED]")
	value = dsnPattern.ReplaceAllString(value, "$1:[REDACTED]@")
	return value
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, strings.TrimSpace(requestID))
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

type requestIDKey struct{}
