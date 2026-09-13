package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxJSONBody  = int64(2 << 20)
	defaultMaxImageBody = int64(64 << 20)
)

type ErrorKind string

const (
	ErrorUnauthenticated     ErrorKind = "unauthenticated"
	ErrorForbidden           ErrorKind = "forbidden"
	ErrorNotFound            ErrorKind = "not_found"
	ErrorRateLimited         ErrorKind = "rate_limited"
	ErrorInvalidRequest      ErrorKind = "invalid_request"
	ErrorUpstreamUnavailable ErrorKind = "upstream_unavailable"
	ErrorInvalidResponse     ErrorKind = "invalid_response"
	ErrorIndeterminate       ErrorKind = "indeterminate"
)

type ContractError struct {
	Kind       ErrorKind
	StatusCode int
	RequestID  string
	cause      error
}

func (e *ContractError) Error() string {
	if e == nil {
		return "official contract error"
	}
	message := "official contract error: " + string(e.Kind)
	if e.StatusCode != 0 {
		message += " (status " + strconv.Itoa(e.StatusCode) + ")"
	}
	if e.RequestID != "" {
		message += " [request_id=" + e.RequestID + "]"
	}
	return message
}

func (e *ContractError) Unwrap() error { return e.cause }

func ErrorKindOf(err error) ErrorKind {
	var contractErr *ContractError
	if errors.As(err, &contractErr) {
		return contractErr.Kind
	}
	return ErrorUpstreamUnavailable
}

type Client struct {
	baseURL      *url.URL
	httpClient   *http.Client
	maxJSONBody  int64
	maxImageBody int64
}

type Option func(*Client)

func WithMaxJSONBody(size int64) Option {
	return func(client *Client) {
		if size > 0 {
			client.maxJSONBody = size
		}
	}
}

func WithMaxImageBody(size int64) Option {
	return func(client *Client) {
		if size > 0 {
			client.maxImageBody = size
		}
	}
}

func NewClient(rawBaseURL string, httpClient *http.Client, options ...Option) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, errors.New("official base URL must be an absolute HTTP URL")
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("official base URL must use http or https")
	}
	if baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("official base URL must not contain credentials, query, or fragment")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	client := &Client{
		baseURL:      baseURL,
		httpClient:   httpClient,
		maxJSONBody:  defaultMaxJSONBody,
		maxImageBody: defaultMaxImageBody,
	}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

type Version struct {
	Version string `json:"version"`
}

type Principal struct {
	ExternalUserID int64  `json:"external_user_id"`
	Role           string `json:"role"`
	Status         string `json:"status"`
}

type APIKeySummary struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	GroupID   *int64  `json:"group_id,omitempty"`
	Status    string  `json:"status"`
	Quota     float64 `json:"quota"`
	QuotaUsed float64 `json:"quota_used"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

type APIKeySecret struct {
	Summary APIKeySummary
	UserID  int64
	Key     []byte
}

type RefreshTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

type ImageResponse struct {
	Body        []byte
	ContentType string
	RequestID   string
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type profilePayload struct {
	ID     int64  `json:"id"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

type rawAPIKey struct {
	ID        int64   `json:"id"`
	UserID    int64   `json:"user_id"`
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	GroupID   *int64  `json:"group_id"`
	Status    string  `json:"status"`
	Quota     float64 `json:"quota"`
	QuotaUsed float64 `json:"quota_used"`
	ExpiresAt *string `json:"expires_at"`
}

func (key rawAPIKey) summary() APIKeySummary {
	return APIKeySummary{
		ID:        key.ID,
		Name:      key.Name,
		GroupID:   key.GroupID,
		Status:    key.Status,
		Quota:     key.Quota,
		QuotaUsed: key.QuotaUsed,
		ExpiresAt: key.ExpiresAt,
	}
}

func (c *Client) PublicVersion(ctx context.Context) (Version, error) {
	var result Version
	if err := c.doEnvelope(ctx, http.MethodGet, "/api/v1/settings/public", "", nil, &result); err != nil {
		return Version{}, err
	}
	result.Version = strings.TrimSpace(result.Version)
	if result.Version == "" || len(result.Version) > 64 {
		return Version{}, invalidResponse(0, "", errors.New("missing or invalid version"))
	}
	return result, nil
}

func (c *Client) Profile(ctx context.Context, bearer string) (Principal, error) {
	if err := validateCredential(bearer, "bearer token"); err != nil {
		return Principal{}, &ContractError{Kind: ErrorUnauthenticated, cause: err}
	}
	var payload profilePayload
	if err := c.doEnvelope(ctx, http.MethodGet, "/api/v1/user/profile", bearer, nil, &payload); err != nil {
		return Principal{}, err
	}
	if payload.ID <= 0 || (payload.Role != "user" && payload.Role != "admin") || payload.Status == "" {
		return Principal{}, invalidResponse(0, "", errors.New("invalid profile fields"))
	}
	if payload.Status != "active" {
		return Principal{}, &ContractError{Kind: ErrorForbidden}
	}
	return Principal{ExternalUserID: payload.ID, Role: payload.Role, Status: payload.Status}, nil
}

func (c *Client) ListKeys(ctx context.Context, bearer string) ([]APIKeySummary, error) {
	if err := validateCredential(bearer, "bearer token"); err != nil {
		return nil, &ContractError{Kind: ErrorUnauthenticated, cause: err}
	}
	var payload struct {
		Items []rawAPIKey `json:"items"`
		Total int64       `json:"total"`
	}
	if err := c.doEnvelope(ctx, http.MethodGet, "/api/v1/keys?page=1&page_size=100", bearer, nil, &payload); err != nil {
		return nil, err
	}
	if payload.Total < 0 || len(payload.Items) > 100 {
		return nil, invalidResponse(0, "", errors.New("invalid key pagination"))
	}
	keys := make([]APIKeySummary, 0, len(payload.Items))
	for _, item := range payload.Items {
		if err := validateKey(item, false); err != nil {
			return nil, invalidResponse(0, "", err)
		}
		keys = append(keys, item.summary())
	}
	return keys, nil
}

func (c *Client) GetKey(ctx context.Context, bearer string, keyID int64) (APIKeySecret, error) {
	if err := validateCredential(bearer, "bearer token"); err != nil {
		return APIKeySecret{}, &ContractError{Kind: ErrorUnauthenticated, cause: err}
	}
	if keyID <= 0 {
		return APIKeySecret{}, &ContractError{Kind: ErrorInvalidRequest}
	}
	var payload rawAPIKey
	path := "/api/v1/keys/" + strconv.FormatInt(keyID, 10)
	if err := c.doEnvelope(ctx, http.MethodGet, path, bearer, nil, &payload); err != nil {
		return APIKeySecret{}, err
	}
	if payload.ID != keyID {
		return APIKeySecret{}, invalidResponse(0, "", errors.New("key ID mismatch"))
	}
	if err := validateKey(payload, true); err != nil {
		return APIKeySecret{}, invalidResponse(0, "", err)
	}
	secret := []byte(payload.Key)
	payload.Key = ""
	return APIKeySecret{Summary: payload.summary(), UserID: payload.UserID, Key: secret}, nil
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (RefreshTokens, error) {
	if err := validateCredential(refreshToken, "refresh token"); err != nil {
		return RefreshTokens{}, &ContractError{Kind: ErrorUnauthenticated, cause: err}
	}
	body, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return RefreshTokens{}, &ContractError{Kind: ErrorInvalidRequest, cause: err}
	}
	var payload RefreshTokens
	if err := c.doEnvelope(ctx, http.MethodPost, "/api/v1/auth/refresh", "", bytes.NewReader(body), &payload); err != nil {
		return RefreshTokens{}, err
	}
	if err := validateCredential(payload.AccessToken, "access token"); err != nil {
		return RefreshTokens{}, invalidResponse(0, "", err)
	}
	if err := validateCredential(payload.RefreshToken, "refresh token"); err != nil {
		return RefreshTokens{}, invalidResponse(0, "", err)
	}
	if payload.ExpiresIn <= 0 || !strings.EqualFold(payload.TokenType, "Bearer") {
		return RefreshTokens{}, invalidResponse(0, "", errors.New("invalid refresh response fields"))
	}
	return payload, nil
}

func (c *Client) DoImageRequest(
	ctx context.Context,
	endpoint string,
	apiKey []byte,
	contentType string,
	body io.Reader,
	idempotencyKey string,
) (ImageResponse, error) {
	if endpoint != "/v1/images/generations" && endpoint != "/v1/images/edits" {
		return ImageResponse{}, &ContractError{Kind: ErrorInvalidRequest}
	}
	if err := validateCredential(string(apiKey), "API key"); err != nil {
		return ImageResponse{}, &ContractError{Kind: ErrorUnauthenticated, cause: err}
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (endpoint == "/v1/images/generations" && mediaType != "application/json") ||
		(endpoint == "/v1/images/edits" && mediaType != "multipart/form-data") {
		return ImageResponse{}, &ContractError{Kind: ErrorInvalidRequest}
	}
	request, err := c.newRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return ImageResponse{}, &ContractError{Kind: ErrorInvalidRequest, cause: err}
	}
	request.Header.Set("Authorization", "Bearer "+string(apiKey))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	if idempotencyKey != "" {
		if len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\r\n") {
			return ImageResponse{}, &ContractError{Kind: ErrorInvalidRequest}
		}
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return ImageResponse{}, &ContractError{Kind: ErrorIndeterminate, cause: errors.New("request outcome unknown")}
	}
	defer response.Body.Close()
	requestID := safeRequestID(response.Header.Get("X-Request-ID"))
	payload, readErr := readBounded(response.Body, c.maxImageBody)
	if readErr != nil {
		return ImageResponse{}, invalidResponse(response.StatusCode, requestID, readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ImageResponse{}, statusError(response.StatusCode, requestID)
	}
	responseType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || (responseType != "application/json" && !strings.HasSuffix(responseType, "+json")) {
		return ImageResponse{}, invalidResponse(response.StatusCode, requestID, errors.New("unexpected image response content type"))
	}
	var shape struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &shape); err != nil || shape.Data == nil {
		return ImageResponse{}, invalidResponse(response.StatusCode, requestID, errors.New("invalid image response JSON"))
	}
	return ImageResponse{Body: payload, ContentType: responseType, RequestID: requestID}, nil
}

func (c *Client) doEnvelope(ctx context.Context, method, path, bearer string, body io.Reader, target any) error {
	request, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return &ContractError{Kind: ErrorInvalidRequest, cause: err}
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return &ContractError{Kind: ErrorUpstreamUnavailable, cause: errors.New("official request failed")}
	}
	defer response.Body.Close()
	requestID := safeRequestID(response.Header.Get("X-Request-ID"))
	payload, readErr := readBounded(response.Body, c.maxJSONBody)
	if readErr != nil {
		return invalidResponse(response.StatusCode, requestID, readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return statusError(response.StatusCode, requestID)
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || (contentType != "application/json" && !strings.HasSuffix(contentType, "+json")) {
		return invalidResponse(response.StatusCode, requestID, errors.New("unexpected response content type"))
	}
	var result envelope
	if err := json.Unmarshal(payload, &result); err != nil {
		return invalidResponse(response.StatusCode, requestID, errors.New("invalid response JSON"))
	}
	if result.Code != 0 || len(result.Data) == 0 || bytes.Equal(result.Data, []byte("null")) {
		if result.Code >= 400 && result.Code <= 599 {
			return statusError(result.Code, requestID)
		}
		return invalidResponse(response.StatusCode, requestID, errors.New("invalid response envelope"))
	}
	if err := json.Unmarshal(result.Data, target); err != nil {
		return invalidResponse(response.StatusCode, requestID, errors.New("invalid response data"))
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("invalid official path")
	}
	requestURL := *c.baseURL
	requestURL.Path = strings.TrimRight(c.baseURL.Path, "/") + strings.SplitN(path, "?", 2)[0]
	if parts := strings.SplitN(path, "?", 2); len(parts) == 2 {
		requestURL.RawQuery = parts[1]
	}
	return http.NewRequestWithContext(ctx, method, requestURL.String(), body)
}

func validateCredential(value, label string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}

func validateKey(key rawAPIKey, requireSecret bool) error {
	if key.ID <= 0 || key.UserID <= 0 || strings.TrimSpace(key.Name) == "" || len(key.Name) > 256 || key.Status == "" {
		return errors.New("invalid API key fields")
	}
	if requireSecret {
		if err := validateCredential(key.Key, "API key"); err != nil {
			return err
		}
	}
	return nil
}

func readBounded(body io.Reader, max int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, errors.New("failed to read bounded response")
	}
	if int64(len(payload)) > max {
		return nil, errors.New("response exceeds size limit")
	}
	return payload, nil
}

func statusError(status int, requestID string) *ContractError {
	kind := ErrorUpstreamUnavailable
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = ErrorInvalidRequest
	case http.StatusUnauthorized:
		kind = ErrorUnauthenticated
	case http.StatusForbidden:
		kind = ErrorForbidden
	case http.StatusNotFound:
		kind = ErrorNotFound
	case http.StatusConflict:
		kind = ErrorInvalidRequest
	case http.StatusTooManyRequests:
		kind = ErrorRateLimited
	}
	return &ContractError{Kind: kind, StatusCode: status, RequestID: requestID}
}

func invalidResponse(status int, requestID string, cause error) *ContractError {
	return &ContractError{Kind: ErrorInvalidResponse, StatusCode: status, RequestID: requestID, cause: cause}
}

func safeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return ""
		}
	}
	return value
}
