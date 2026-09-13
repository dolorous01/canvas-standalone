package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dolorous01/canvas-standalone/backend/internal/authn"
	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
)

func TestRequireAllowedUser(t *testing.T) {
	server := &server{allowedUsers: map[int64]struct{}{42: {}}}
	called := false
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		called = true
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := server.requireAllowedUser(next)

	request := httptest.NewRequest(http.MethodGet, "/canvas-api/v1/session", nil)
	request = request.WithContext(authn.WithSession(request.Context(), authn.Session{
		Principal: gateway.Principal{ExternalUserID: 84, Role: "admin", Status: "active"},
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("unexpected denied response: status=%d called=%v", response.Code, called)
	}

	request = httptest.NewRequest(http.MethodGet, "/canvas-api/v1/session", nil)
	request = request.WithContext(authn.WithSession(request.Context(), authn.Session{
		Principal: gateway.Principal{ExternalUserID: 42, Role: "user", Status: "active"},
	}))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("unexpected allowed response: status=%d called=%v", response.Code, called)
	}
}
