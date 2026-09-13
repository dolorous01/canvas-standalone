package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientContracts(t *testing.T) {
	const bearer = "valid-browser-bearer"
	const refresh = "valid-refresh-token"
	const apiKey = "sk-" + "valid-studio-key"
	var imageCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Request-ID", "official-request-1")
		switch request.URL.Path {
		case "/api/v1/settings/public":
			fmt.Fprint(writer, `{"code":0,"message":"success","data":{"version":"0.2.4"}}`)
		case "/api/v1/user/profile":
			if request.Header.Get("Authorization") != "Bearer "+bearer {
				writer.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(writer, `{"code":401,"message":"no"}`)
				return
			}
			fmt.Fprint(writer, `{"code":0,"message":"success","data":{"id":42,"role":"admin","status":"active"}}`)
		case "/api/v1/keys":
			fmt.Fprint(writer, `{"code":0,"message":"success","data":{"items":[{"id":7,"user_id":42,"key":"must-not-leave-list","name":"Studio","status":"active","quota":5,"quota_used":1}],"total":1,"page":1,"page_size":100,"pages":1}}`)
		case "/api/v1/keys/7":
			fmt.Fprint(writer, `{"code":0,"message":"success","data":{"id":7,"user_id":42,"key":"`+apiKey+`","name":"Studio","status":"active","quota":5,"quota_used":1}}`)
		case "/api/v1/auth/refresh":
			body, _ := io.ReadAll(request.Body)
			if !bytes.Contains(body, []byte(refresh)) {
				writer.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(writer, `{"code":401,"message":"no"}`)
				return
			}
			fmt.Fprint(writer, `{"code":0,"message":"success","data":{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600,"token_type":"Bearer"}}`)
		case "/v1/images/generations":
			imageCalls.Add(1)
			if request.Header.Get("Authorization") != "Bearer "+apiKey || request.Header.Get("Idempotency-Key") != "job-1" {
				writer.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(writer, `{"error":{"message":"no"}}`)
				return
			}
			fmt.Fprint(writer, `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
			fmt.Fprint(writer, `{"code":404,"message":"missing"}`)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	version, err := client.PublicVersion(ctx)
	if err != nil || version.Version != "0.2.4" {
		t.Fatalf("version = %+v, err = %v", version, err)
	}
	principal, err := client.Profile(ctx, bearer)
	if err != nil || principal.ExternalUserID != 42 || principal.Role != "admin" {
		t.Fatalf("principal = %+v, err = %v", principal, err)
	}
	keys, err := client.ListKeys(ctx, bearer)
	if err != nil || len(keys) != 1 || keys[0].ID != 7 {
		t.Fatalf("keys = %+v, err = %v", keys, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", keys), "must-not-leave-list") {
		t.Fatal("list response leaked a full key")
	}
	secret, err := client.GetKey(ctx, bearer, 7)
	if err != nil || secret.UserID != principal.ExternalUserID || string(secret.Key) != apiKey {
		t.Fatalf("key metadata mismatch, err = %v", err)
	}
	tokens, err := client.Refresh(ctx, refresh)
	if err != nil || tokens.ExpiresIn != 3600 || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("refresh response invalid, err = %v", err)
	}
	image, err := client.DoImageRequest(ctx, "/v1/images/generations", secret.Key, "application/json", strings.NewReader(`{"model":"test"}`), "job-1")
	if err != nil || !bytes.Contains(image.Body, []byte(`"data"`)) || imageCalls.Load() != 1 {
		t.Fatalf("image response invalid, calls = %d, err = %v", imageCalls.Load(), err)
	}
}

func TestClientRejectsInvalidResponsesWithoutLeakingSecrets(t *testing.T) {
	const sentinel = "sk-" + "this-secret-must-never-appear"
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		maxBody     int64
		want        ErrorKind
	}{
		{name: "unauthorized", status: 401, contentType: "application/json", body: `{"message":"` + sentinel + `"}`, want: ErrorUnauthenticated},
		{name: "rate limited", status: 429, contentType: "application/json", body: `{"message":"` + sentinel + `"}`, want: ErrorRateLimited},
		{name: "server error", status: 503, contentType: "application/json", body: `{"message":"` + sentinel + `"}`, want: ErrorUpstreamUnavailable},
		{name: "html", status: 200, contentType: "text/html", body: `<p>` + sentinel + `</p>`, want: ErrorInvalidResponse},
		{name: "malformed", status: 200, contentType: "application/json", body: `{` + sentinel, want: ErrorInvalidResponse},
		{name: "oversized", status: 200, contentType: "application/json", body: `{"code":0,"data":{"version":"0.2.4"}}`, maxBody: 8, want: ErrorInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()
			options := []Option{}
			if test.maxBody != 0 {
				options = append(options, WithMaxJSONBody(test.maxBody))
			}
			client, err := NewClient(server.URL, &http.Client{Timeout: time.Second}, options...)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.PublicVersion(context.Background())
			if err == nil || ErrorKindOf(err) != test.want {
				t.Fatalf("error = %v, kind = %q", err, ErrorKindOf(err))
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("error leaked upstream body: %v", err)
			}
		})
	}
}

func TestClientMapsTransportOutcomeByOperation(t *testing.T) {
	transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errorsForTest("dial failed with sk-secret-sentinel")
	})
	client, err := NewClient("http://127.0.0.1:1", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, profileErr := client.Profile(context.Background(), "browser-token")
	if ErrorKindOf(profileErr) != ErrorUpstreamUnavailable {
		t.Fatalf("profile error = %v", profileErr)
	}
	_, imageErr := client.DoImageRequest(context.Background(), "/v1/images/generations", []byte("sk-test-key"), "application/json", strings.NewReader(`{}`), "job")
	if ErrorKindOf(imageErr) != ErrorIndeterminate {
		t.Fatalf("image error = %v", imageErr)
	}
	if strings.Contains(profileErr.Error()+imageErr.Error(), "sentinel") {
		t.Fatal("transport error leaked into public error")
	}
}

func TestNewClientRejectsUnsafeBaseURLs(t *testing.T) {
	for _, value := range []string{"", "ftp://example.test", "https://user:pass@example.test", "https://example.test?q=1"} {
		if _, err := NewClient(value, nil); err == nil {
			t.Fatalf("NewClient(%q) succeeded", value)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type errorsForTest string

func (err errorsForTest) Error() string { return string(err) }
