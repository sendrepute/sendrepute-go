// Package sendrepute provides a dependency-free, server-only client for the
// paid SendRepute customer classification API.
package sendrepute

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultEndpoint = "https://www.sendrepute.com/api"

	maxSenderBytes   = 320 * 4
	maxSubjectBytes  = 998 * 4
	maxBodyBytes     = 524288
	maxPayloadBytes  = 768 * 1024
	maxResponseBytes = 1048576
	maxTimeout       = 30 * time.Second
)

var supportedModels = map[Model]bool{
	ModelThor: true, ModelTheos: true, ModelAthena: true, ModelOdin: true,
	ModelFreya: true, ModelHermes: true, ModelAres: true, ModelApollo: true,
}

// Config configures a Client. PaidAnalysisConsent intentionally defaults to
// false.
type Config struct {
	APIKey              string
	Endpoint            string
	AllowedOrigins      []string
	Timeout             time.Duration
	PaidAnalysisConsent bool
	transport           http.RoundTripper
}

// Client is immutable after construction and safe for concurrent use when its
// RoundTripper is safe for concurrent use (as net/http transports are).
type Client struct {
	apiKey      string
	endpoint    *url.URL
	timeout     time.Duration
	paidConsent bool
	http        *http.Client
}

type ErrorKind string

const (
	KindConsentRequired  ErrorKind = "CONSENT_REQUIRED"
	KindInvalidInput     ErrorKind = "INVALID_INPUT"
	KindTransport        ErrorKind = "TRANSPORT_ERROR"
	KindTimeout          ErrorKind = "REQUEST_TIMEOUT"
	KindCancelled        ErrorKind = "REQUEST_CANCELLED"
	KindRedirect         ErrorKind = "REDIRECT_REFUSED"
	KindInvalidResponse  ErrorKind = "INVALID_RESPONSE"
	KindResponseTooLarge ErrorKind = "RESPONSE_TOO_LARGE"
	KindAuthentication   ErrorKind = "AUTHENTICATION_ERROR"
	KindBalance          ErrorKind = "BALANCE_ERROR"
	KindRateLimit        ErrorKind = "RATE_LIMIT_ERROR"
	KindClassification   ErrorKind = "CLASSIFICATION_ERROR"
)

// Error contains only bounded, redacted metadata. It never includes the API
// key, message content, response body, network address, or server error text.
type Error struct {
	Kind      ErrorKind
	Code      string
	Status    int
	RequestID string
	message   string
}

func (e *Error) Error() string { return e.message }

// NewClient validates all configuration before retaining the API key.
func NewClient(config Config) (*Client, error) {
	if strings.TrimSpace(config.APIKey) == "" || strings.ContainsAny(config.APIKey, "\r\n") {
		return nil, errors.New("api key is required and must be a valid header value")
	}
	endpoint := config.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	allowed := config.AllowedOrigins
	if len(allowed) == 0 {
		allowed = []string{"https://www.sendrepute.com"}
	}
	parsed, err := validateEndpoint(endpoint, allowed)
	if err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if timeout <= 0 || timeout > maxTimeout {
		return nil, errors.New("timeout must be greater than zero and at most 30 seconds")
	}
	transport := config.transport
	if transport == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		transport = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   5,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		}
	}
	return &Client{
		apiKey: config.APIKey, endpoint: parsed, timeout: timeout,
		paidConsent: config.PaidAnalysisConsent,
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func validateEndpoint(raw string, allowed []string) (*url.URL, error) {
	value, err := url.Parse(raw)
	if err != nil || value.Scheme != "https" || value.Hostname() == "" || value.User != nil ||
		value.RawQuery != "" || value.Fragment != "" || value.Opaque != "" {
		return nil, errors.New("endpoint must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	origin := canonicalOrigin(value)
	trusted := false
	for _, item := range allowed {
		candidate, parseErr := url.Parse(item)
		if parseErr != nil || candidate.Scheme != "https" || candidate.Hostname() == "" ||
			candidate.User != nil || candidate.RawQuery != "" || candidate.Fragment != "" ||
			candidate.Opaque != "" || (candidate.Path != "" && candidate.Path != "/") {
			return nil, errors.New("allowed origins must be HTTPS origins without paths or credentials")
		}
		if canonicalOrigin(candidate) == origin {
			trusted = true
		}
	}
	if !trusted {
		return nil, errors.New("endpoint origin is not explicitly trusted")
	}
	if strings.EqualFold(value.Hostname(), "localhost") {
		return nil, errors.New("loopback endpoints are not allowed")
	}
	if ip := net.ParseIP(value.Hostname()); ip != nil && ip.IsLoopback() {
		return nil, errors.New("loopback endpoints are not allowed")
	}
	if value.RawPath != "" {
		return nil, errors.New("endpoint path must use its canonical encoding")
	}
	value.Path = strings.TrimRight(value.Path, "/")
	if value.Path == "" || strings.Contains(value.Path, "\\") {
		return nil, errors.New("endpoint must include a fixed API path")
	}
	return value, nil
}

func canonicalOrigin(value *url.URL) string {
	host := strings.ToLower(value.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := value.Port()
	if port != "" && port != "443" {
		host += ":" + port
	}
	return "https://" + host
}

type requestPayload struct {
	Sender  string `json:"sender"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Model   Model  `json:"model,omitempty"`
}

// Classify performs exactly one paid request when consent is true. It never
// retries. Per-call consent can opt in when client-level consent is false.
func (c *Client) Classify(ctx context.Context, input ClassificationInput, paidConsent bool) (*ClassificationResponse, error) {
	if !c.paidConsent && !paidConsent {
		return nil, sdkError(KindConsentRequired, "CONSENT_REQUIRED", 0, "", "paid classification requires explicit consent")
	}
	if ctx == nil {
		return nil, sdkError(KindInvalidInput, "INVALID_INPUT", 0, "", "context is required")
	}
	if err := validateInput(input); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestPayload(input))
	if err != nil || len(payload) > maxPayloadBytes {
		return nil, sdkError(KindInvalidInput, "INVALID_INPUT", 0, "", "encoded request is too large")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	target := *c.endpoint
	target.Path = strings.TrimRight(target.Path, "/") + "/v1/classify"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, sdkError(KindInvalidInput, "INVALID_INPUT", 0, "", "classification request is invalid")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	response, err := c.http.Do(req)
	if err != nil {
		if requestCtx.Err() != nil {
			if ctx.Err() != nil {
				return nil, sdkError(KindCancelled, "REQUEST_CANCELLED", 0, "", "SendRepute request was cancelled")
			}
			return nil, sdkError(KindTimeout, "REQUEST_TIMEOUT", 0, "", "SendRepute request timed out")
		}
		return nil, sdkError(KindTransport, "TRANSPORT_ERROR", 0, "", "SendRepute transport request failed")
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, sdkError(KindRedirect, "REDIRECT_REFUSED", response.StatusCode, "", "SendRepute redirects are refused")
	}
	if response.ContentLength > maxResponseBytes {
		return nil, sdkError(KindResponseTooLarge, "RESPONSE_TOO_LARGE", response.StatusCode, "", "SendRepute response was too large")
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil {
		return nil, sdkError(KindTransport, "TRANSPORT_ERROR", response.StatusCode, "", "SendRepute response could not be read")
	}
	if len(raw) > maxResponseBytes {
		return nil, sdkError(KindResponseTooLarge, "RESPONSE_TOO_LARGE", response.StatusCode, "", "SendRepute response was too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeAPIError(response.StatusCode, raw)
	}
	var result ClassificationResponse
	if err := validateResponseShape(raw); err != nil {
		return nil, sdkError(KindInvalidResponse, "INVALID_RESPONSE", response.StatusCode, "", "SendRepute returned an invalid response")
	}
	if err := decodeStrict(raw, &result); err != nil || validateResponse(&result) != nil {
		return nil, sdkError(KindInvalidResponse, "INVALID_RESPONSE", response.StatusCode, "", "SendRepute returned an invalid response")
	}
	return &result, nil
}

func validateInput(input ClassificationInput) error {
	if err := boundedString(input.Sender, "sender", 320, maxSenderBytes); err != nil {
		return err
	}
	if err := boundedString(input.Subject, "subject", 998, maxSubjectBytes); err != nil {
		return err
	}
	if err := boundedString(input.Body, "body", 524288, maxBodyBytes); err != nil {
		return err
	}
	if input.Model != "" && !supportedModels[input.Model] {
		return sdkError(KindInvalidInput, "INVALID_INPUT", 0, "", "model is not supported")
	}
	return nil
}

func boundedString(value, name string, runes, bytesLimit int) error {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > runes || len(value) > bytesLimit {
		return sdkError(KindInvalidInput, "INVALID_INPUT", 0, "", name+" must be a non-empty bounded UTF-8 string")
	}
	return nil
}

func decodeStrict(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("response contains trailing data")
	}
	return nil
}

func validateResponseShape(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	top, err := exactObject(value, []string{"requestId", "model", "result", "billing"}, nil)
	if err != nil {
		return err
	}
	if !isString(top["requestId"]) || !isString(top["model"]) {
		return errors.New("invalid envelope primitive")
	}
	billing, err := exactObject(top["billing"], []string{"chargedMillicents", "replayed"}, nil)
	if err != nil {
		return err
	}
	if !isNumber(billing["chargedMillicents"]) || !isBool(billing["replayed"]) {
		return errors.New("invalid billing primitive")
	}
	result, err := exactObject(top["result"],
		[]string{"label", "spamProbability", "confidence", "reasons", "flaggedTerms", "analyzedFields", "modelVersion", "analyzedAt"},
		[]string{"flaggedTermCount", "contentAudit"})
	if err != nil {
		return err
	}
	for _, key := range []string{"label", "confidence", "modelVersion", "analyzedAt"} {
		if !isString(result[key]) {
			return errors.New("invalid classification string")
		}
	}
	if !isNumber(result["spamProbability"]) {
		return errors.New("invalid classification number")
	}
	if count, present := result["flaggedTermCount"]; present && !isNumber(count) {
		return errors.New("invalid flagged count")
	}
	reasons, ok := result["reasons"].([]any)
	if !ok {
		return errors.New("reasons must be an array")
	}
	for _, reason := range reasons {
		item, err := exactObject(reason, []string{"signal", "detail", "weight"}, nil)
		if err != nil {
			return err
		}
		if !isString(item["signal"]) || !isString(item["detail"]) || !isNumber(item["weight"]) {
			return errors.New("invalid reason primitive")
		}
	}
	for _, key := range []string{"flaggedTerms", "analyzedFields"} {
		items, ok := result[key].([]any)
		if !ok {
			return errors.New("classification string list must be an array")
		}
		for _, item := range items {
			if !isString(item) {
				return errors.New("classification string list contains an invalid value")
			}
		}
	}
	if auditValue, ok := result["contentAudit"]; ok {
		audit, err := exactObject(auditValue,
			[]string{"score", "grade", "summary", "counts", "totalIssues", "criticalCount", "warningCount", "suggestionCount", "issues", "goodPractices", "inputTruncated"},
			[]string{"homoglyphTerms"})
		if err != nil {
			return err
		}
		for _, key := range []string{"grade", "summary"} {
			if !isString(audit[key]) {
				return errors.New("invalid audit string")
			}
		}
		for _, key := range []string{"score", "totalIssues", "criticalCount", "warningCount", "suggestionCount"} {
			if !isNumber(audit[key]) {
				return errors.New("invalid audit number")
			}
		}
		if !isBool(audit["inputTruncated"]) {
			return errors.New("invalid audit boolean")
		}
		counts, err := exactObject(audit["counts"], []string{"words", "links", "images", "triggerPhrases"}, nil)
		if err != nil {
			return err
		}
		for _, key := range []string{"words", "links", "images", "triggerPhrases"} {
			if !isNumber(counts[key]) {
				return errors.New("invalid audit count")
			}
		}
		issues, ok := audit["issues"].([]any)
		if !ok {
			return errors.New("issues must be an array")
		}
		for _, issue := range issues {
			item, err := exactObject(issue, []string{"code", "category", "severity", "deduction", "evidence"}, nil)
			if err != nil {
				return err
			}
			if !isString(item["code"]) || !isString(item["category"]) || !isString(item["severity"]) ||
				!isNumber(item["deduction"]) || !isString(item["evidence"]) {
				return errors.New("invalid audit issue primitive")
			}
		}
		practices, ok := audit["goodPractices"].([]any)
		if !ok {
			return errors.New("good practices must be an array")
		}
		for _, practice := range practices {
			item, err := exactObject(practice, []string{"code", "category"}, nil)
			if err != nil {
				return err
			}
			if !isString(item["code"]) || !isString(item["category"]) {
				return errors.New("invalid audit practice primitive")
			}
		}
		if termsValue, present := audit["homoglyphTerms"]; present {
			terms, ok := termsValue.([]any)
			if !ok {
				return errors.New("homoglyph terms must be an array")
			}
			for _, term := range terms {
				if !isString(term) {
					return errors.New("invalid homoglyph term")
				}
			}
		}
	}
	return nil
}

func isString(value any) bool {
	_, ok := value.(string)
	return ok
}

func isNumber(value any) bool {
	_, ok := value.(json.Number)
	return ok
}

func isBool(value any) bool {
	_, ok := value.(bool)
	return ok
}

func exactObject(value any, required, optional []string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("value must be an object")
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
		if _, present := object[key]; !present {
			return nil, errors.New("required response field is missing")
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return nil, errors.New("unexpected response field")
		}
	}
	return object, nil
}

func validateResponse(value *ClassificationResponse) error {
	if value.RequestID == "" || len(value.RequestID) > 128 || !supportedModels[value.Model] {
		return errors.New("invalid envelope")
	}
	if value.Billing.ChargedMillicents < 0 {
		return errors.New("invalid billing")
	}
	r := &value.Result
	if (r.Label != "inbox" && r.Label != "spam") ||
		(r.Confidence != "low" && r.Confidence != "medium" && r.Confidence != "high") ||
		math.IsNaN(r.SpamProbability) || math.IsInf(r.SpamProbability, 0) ||
		r.SpamProbability < 0 || r.SpamProbability > 1 {
		return errors.New("invalid classification")
	}
	if r.FlaggedTermCount != nil && *r.FlaggedTermCount < 0 {
		return errors.New("invalid flagged count")
	}
	if r.Reasons == nil || r.FlaggedTerms == nil || r.AnalyzedFields == nil || r.ModelVersion == "" || r.AnalyzedAt == "" {
		return errors.New("missing classification field")
	}
	for _, reason := range r.Reasons {
		if math.IsNaN(reason.Weight) || math.IsInf(reason.Weight, 0) {
			return errors.New("invalid reason")
		}
	}
	if r.ContentAudit != nil {
		return validateAudit(r.ContentAudit)
	}
	return nil
}

func validateAudit(a *ContentAudit) error {
	validGrade := a.Grade == "A" || a.Grade == "B" || a.Grade == "C" || a.Grade == "D" || a.Grade == "F"
	validSummary := a.Summary == "fix_critical" || a.Summary == "fix_warnings" || a.Summary == "review_suggestions" || a.Summary == "looks_good"
	if a.Score < 0 || a.Score > 100 || !validGrade || !validSummary || a.TotalIssues < 0 ||
		a.CriticalCount < 0 || a.WarningCount < 0 || a.SuggestionCount < 0 ||
		a.Counts.Words < 0 || a.Counts.Links < 0 || a.Counts.Images < 0 || a.Counts.TriggerPhrases < 0 ||
		a.Issues == nil || len(a.Issues) > 50 || a.GoodPractices == nil || len(a.GoodPractices) > 20 ||
		len(a.HomoglyphTerms) > 20 {
		return errors.New("invalid content audit")
	}
	for _, issue := range a.Issues {
		if !validCategory(issue.Category) || (issue.Severity != "critical" && issue.Severity != "warning" && issue.Severity != "suggestion") ||
			issue.Deduction < 0 || issue.Deduction > 100 || utf8.RuneCountInString(issue.Evidence) > 200 {
			return errors.New("invalid content audit issue")
		}
	}
	for _, practice := range a.GoodPractices {
		if !validCategory(practice.Category) {
			return errors.New("invalid content audit practice")
		}
	}
	for _, term := range a.HomoglyphTerms {
		if utf8.RuneCountInString(term) > 120 {
			return errors.New("invalid homoglyph term")
		}
	}
	return nil
}

func validCategory(value string) bool {
	return value == "subject" || value == "content" || value == "links" || value == "structure" || value == "compliance"
}

type apiErrorPayload struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
	RequestID string `json:"requestId"`
}

func decodeAPIError(status int, raw []byte) error {
	code, requestID := "API_ERROR", ""
	var payload apiErrorPayload
	if json.Unmarshal(raw, &payload) == nil {
		if safeCode(payload.Error.Code) {
			code = payload.Error.Code
		}
		if safeIdentifier(payload.RequestID) {
			requestID = payload.RequestID
		}
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return sdkError(KindAuthentication, code, status, requestID, "SendRepute authentication or permission failed")
	case http.StatusPaymentRequired:
		return sdkError(KindBalance, code, status, requestID, "SendRepute account balance or spend limit is insufficient")
	case http.StatusTooManyRequests:
		return sdkError(KindRateLimit, code, status, requestID, "SendRepute rate limit was exceeded")
	default:
		return sdkError(KindClassification, code, status, requestID, fmt.Sprintf("SendRepute classification request failed (status %d)", status))
	}
}

func safeCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func sdkError(kind ErrorKind, code string, status int, requestID, message string) *Error {
	return &Error{Kind: kind, Code: code, Status: status, RequestID: requestID, message: message}
}
