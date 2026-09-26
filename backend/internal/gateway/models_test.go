package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestModelDiscoveryContract(t *testing.T) {
	key := "sk-" + "discovery-private"
	for _, tc := range []struct {
		name, body, content string
		status              int
		fail                bool
	}{
		{"models", `{"data":[{"id":"gpt-image-2.5-flare","secret":"discard"},{"id":"gpt-image-2.5-flare"},{"id":"gpt-5"}]}`, "application/json", 200, false},
		{"empty", `{"data":[]}`, "application/json", 200, false},
		{"null", `{"data":null}`, "application/json", 200, true},
		{"missing id", `{"data":[{}]}`, "application/json", 200, true},
		{"echoed key", `{"data":[{"id":"` + key + `"}]}`, "application/json", 200, true},
		{"html", "<html>" + key, "text/html", 200, true},
		{"oversized", strings.Repeat("x", 300), "application/json", 200, true},
		{"unauthorized", key, "application/json", 401, true},
		{"rate limit", key, "application/json", 429, true},
		{"redirect", key, "application/json", 302, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer "+key {
					t.Error("wrong discovery request")
				}
				w.Header().Set("Content-Type", tc.content)
				w.Header().Set("Location", "/do-not-follow")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer upstream.Close()
			client, _ := NewClient(upstream.URL, &http.Client{Timeout: time.Second}, WithMaxJSONBody(256))
			models, err := client.ListModels(context.Background(), []byte(key))
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if calls != 1 {
				t.Fatalf("followed redirect or sent extra request: %d", calls)
			}
			if err != nil && strings.Contains(err.Error(), key) {
				t.Fatal("key leaked")
			}
			if tc.name == "models" && (len(models) != 2 || models[0] != "gpt-image-2.5-flare") {
				t.Fatalf("models: %v", models)
			}
		})
	}
}
