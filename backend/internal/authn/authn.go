package authn

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
)

var ErrMissingBearer = errors.New("official bearer token is missing or invalid")

type ProfileClient interface {
	Profile(context.Context, string) (gateway.Principal, error)
}

type Session struct {
	Principal gateway.Principal
	Bearer    string
}

type Authenticator struct {
	client     ProfileClient
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
	mu         sync.Mutex
	cache      map[[sha256.Size]byte]cacheEntry
}

type cacheEntry struct {
	principal gateway.Principal
	expiresAt time.Time
}

func New(client ProfileClient, ttl time.Duration, maxEntries int) *Authenticator {
	if ttl < 0 || ttl > 30*time.Second {
		ttl = 20 * time.Second
	}
	if maxEntries <= 0 {
		maxEntries = 2048
	}
	return &Authenticator{client: client, ttl: ttl, maxEntries: maxEntries, now: time.Now, cache: make(map[[sha256.Size]byte]cacheEntry)}
}

func (auth *Authenticator) AuthenticateRequest(ctx context.Context, header string, fresh bool) (Session, error) {
	token, err := ExtractBearer(header)
	if err != nil {
		return Session{}, err
	}
	principal, err := auth.AuthenticateToken(ctx, token, fresh)
	if err != nil {
		return Session{}, err
	}
	return Session{Principal: principal, Bearer: token}, nil
}

func (auth *Authenticator) AuthenticateToken(ctx context.Context, token string, fresh bool) (gateway.Principal, error) {
	if strings.TrimSpace(token) == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n") {
		return gateway.Principal{}, ErrMissingBearer
	}
	digest := sha256.Sum256([]byte(token))
	if !fresh && auth.ttl > 0 {
		auth.mu.Lock()
		entry, ok := auth.cache[digest]
		if ok && auth.now().Before(entry.expiresAt) {
			auth.mu.Unlock()
			return entry.principal, nil
		}
		if ok {
			delete(auth.cache, digest)
		}
		auth.mu.Unlock()
	}
	principal, err := auth.client.Profile(ctx, token)
	if err != nil {
		auth.mu.Lock()
		delete(auth.cache, digest)
		auth.mu.Unlock()
		return gateway.Principal{}, err
	}
	if auth.ttl > 0 {
		auth.mu.Lock()
		auth.evictExpiredOrOldest()
		auth.cache[digest] = cacheEntry{principal: principal, expiresAt: auth.now().Add(auth.ttl)}
		auth.mu.Unlock()
	}
	return principal, nil
}

func (auth *Authenticator) Invalidate(token string) {
	digest := sha256.Sum256([]byte(token))
	auth.mu.Lock()
	delete(auth.cache, digest)
	auth.mu.Unlock()
}

func (auth *Authenticator) evictExpiredOrOldest() {
	if len(auth.cache) < auth.maxEntries {
		return
	}
	now := auth.now()
	var oldestKey [sha256.Size]byte
	var oldest time.Time
	for key, entry := range auth.cache {
		if !now.Before(entry.expiresAt) {
			delete(auth.cache, key)
			if len(auth.cache) < auth.maxEntries {
				return
			}
		}
		if oldest.IsZero() || entry.expiresAt.Before(oldest) {
			oldestKey, oldest = key, entry.expiresAt
		}
	}
	delete(auth.cache, oldestKey)
}

func ExtractBearer(header string) (string, error) {
	if header == "" || strings.ContainsAny(header, "\r\n") {
		return "", ErrMissingBearer
	}
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 4096 {
		return "", ErrMissingBearer
	}
	return parts[1], nil
}

func Middleware(auth *Authenticator, onError func(http.ResponseWriter, *http.Request, error), next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		session, err := auth.AuthenticateRequest(request.Context(), request.Header.Get("Authorization"), false)
		if err != nil {
			onError(writer, request, err)
			return
		}
		next.ServeHTTP(writer, request.WithContext(WithSession(request.Context(), session)))
	})
}

func WithSession(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

func FromContext(ctx context.Context) (Session, bool) {
	session, ok := ctx.Value(sessionKey{}).(Session)
	return session, ok
}

type sessionKey struct{}
