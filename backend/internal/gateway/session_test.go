package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProfilePreservesSessionBindingWithoutOtherCredentials(t *testing.T) {
	headers := http.Header{}
	headers.Set("User-Agent", "original-browser")
	headers.Set("CF-Connecting-IP", "203.0.113.7")
	headers.Set("X-Real-IP", "203.0.113.7")
	headers.Set("X-Forwarded-For", "203.0.113.7, 127.0.0.1")
	headers.Set("Cookie", "unrelated-session=secret")
	headers.Set("Authorization", "Bearer unrelated")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"User-Agent", "CF-Connecting-IP", "X-Real-IP", "X-Forwarded-For"} {
			if r.Header.Get(name) != headers.Get(name) {
				t.Errorf("session binding header %s was lost", name)
			}
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "Bearer expected-token" {
			t.Error("unrelated credentials forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":0,"data":{"id":42,"role":"admin","status":"active"}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSessionHeaders(context.Background(), headers)
	if _, err := client.Profile(ctx, "expected-token"); err != nil {
		t.Fatal(err)
	}
}
