package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// ListModels queries metadata only, not paid image generation.
func (c *Client) ListModels(ctx context.Context, apiKey []byte) ([]string, error) {
	if err := validateCredential(string(apiKey), "API key"); err != nil {
		return nil, &ContractError{Kind: ErrorUnauthenticated}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, &ContractError{Kind: ErrorInvalidRequest}
	}
	request.Header.Set("Authorization", "Bearer "+string(apiKey))
	request.Header.Set("Accept", "application/json")
	forwardSessionHeaders(ctx, request)
	// Never redirect a credential-bearing request or mutate the shared client.
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, &ContractError{Kind: ErrorUpstreamUnavailable}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError(response.StatusCode, "")
	}
	payload, err := readBounded(response.Body, c.maxJSONBody)
	if err != nil {
		return nil, invalidResponse(response.StatusCode, "", err)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return nil, invalidResponse(200, "", errors.New("unexpected model response type"))
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &catalog); err != nil || catalog.Data == nil || len(catalog.Data) > 5000 {
		return nil, invalidResponse(200, "", errors.New("invalid model catalogue"))
	}
	models := make([]string, 0, len(catalog.Data))
	seen := make(map[string]bool)
	for _, entry := range catalog.Data {
		id := entry.ID
		if id == "" || len(id) > 128 || strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || strings.Contains(id, string(apiKey)) {
			return nil, invalidResponse(200, "", errors.New("invalid model ID"))
		}
		if !seen[strings.ToLower(id)] {
			models = append(models, id)
			seen[strings.ToLower(id)] = true
		}
	}
	return models, nil
}
