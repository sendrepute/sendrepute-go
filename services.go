package sendrepute

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ServiceInput keeps URL destinations fixed by the customer contract.
// AuthorizeMutation must be set explicitly per request; client-wide mail
// classification consent never authorizes unrelated paid services.
type ServiceInput struct {
	Body              map[string]any
	Path              map[string]string
	Query             url.Values
	AuthorizeMutation bool
}

// Request executes one customer operation without retrying. Obtain a quote
// before submitting paid fields; preserve result IDs for recovery.
func (c *Client) Request(ctx context.Context, operation string, input ServiceInput) (json.RawMessage, error) {
	metadata, ok := serviceOperations[operation]
	if !ok || ctx == nil {
		return nil, sdkError(KindInvalidInput, "INVALID_OPERATION", 0, "", "unknown customer operation")
	}
	if metadata.Method != "GET" && !input.AuthorizeMutation {
		return nil, sdkError(KindConsentRequired, "CONSENT_REQUIRED", 0, "", "explicit operation authorization required")
	}
	if len(input.Path) != len(metadata.PathParameters) {
		return nil, sdkError(KindInvalidInput, "INVALID_PATH", 0, "", "incorrect path parameters")
	}
	route := metadata.Path
	for _, key := range metadata.PathParameters {
		value := input.Path[key]
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "/\\?#") || value == "." || value == ".." {
			return nil, sdkError(KindInvalidInput, "INVALID_PATH", 0, "", "invalid path parameter")
		}
		route = strings.ReplaceAll(route, "{"+key+"}", url.PathEscape(value))
	}
	for key := range input.Query {
		allowed := false
		for _, candidate := range metadata.Query {
			if key == candidate {
				allowed = true
			}
		}
		if !allowed {
			return nil, sdkError(KindInvalidInput, "INVALID_QUERY", 0, "", "unsupported query parameter")
		}
	}
	var payload []byte
	var err error
	if input.Body == nil && (metadata.Method == "POST" || metadata.Method == "PUT" || metadata.Method == "PATCH") {
		input.Body = map[string]any{}
	}
	if input.Body != nil {
		if metadata.Method == "GET" {
			return nil, sdkError(KindInvalidInput, "INVALID_BODY", 0, "", "GET cannot have a body")
		}
		payload, err = json.Marshal(input.Body)
		if err != nil || len(payload) > maxPayloadBytes {
			return nil, sdkError(KindInvalidInput, "INVALID_BODY", 0, "", "invalid or oversized body")
		}
	}
	target := *c.endpoint
	escaped := strings.TrimRight(target.EscapedPath(), "/") + route
	target.Path, err = url.PathUnescape(escaped)
	if err != nil {
		return nil, sdkError(KindInvalidInput, "INVALID_PATH", 0, "", "invalid path")
	}
	target.RawPath = escaped
	target.RawQuery = input.Query.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, metadata.Method, target.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, sdkError(KindInvalidInput, "INVALID_REQUEST", 0, "", "invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, sdkError(KindTransport, "TRANSPORT_ERROR", 0, "", "service transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, sdkError(KindRedirect, "REDIRECT_REFUSED", response.StatusCode, "", "redirect refused")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, sdkError(KindInvalidResponse, "INVALID_RESPONSE", response.StatusCode, "", "invalid or oversized response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeAPIError(response.StatusCode, raw)
	}
	if response.StatusCode == 204 {
		return json.RawMessage("null"), nil
	}
	if !json.Valid(raw) {
		return nil, sdkError(KindInvalidResponse, "INVALID_RESPONSE", response.StatusCode, "", "invalid JSON")
	}
	return json.RawMessage(raw), nil
}
