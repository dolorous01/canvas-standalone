package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerRedactsSensitiveKeysAndValues(t *testing.T) {
	const bearer = "Bearer abc.def.very-secret"
	const apiKey = "sk-sensitive-value"
	const password = "database-password"
	var output bytes.Buffer
	logger := New(&output, slog.LevelDebug)
	logger.Error("request failed "+bearer+" "+apiKey,
		"authorization", bearer,
		"database_url", "postgres://canvas:"+password+"@database/canvas",
		"safe", "request-1",
	)
	value := output.String()
	for _, secret := range []string{"abc.def.very-secret", "sensitive-value", password} {
		if strings.Contains(value, secret) {
			t.Fatalf("log leaked %q: %s", secret, value)
		}
	}
	if !strings.Contains(value, "request-1") {
		t.Fatalf("safe value missing: %s", value)
	}
}
