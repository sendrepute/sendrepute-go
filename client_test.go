package sendrepute

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

const validJSON = `{
  "requestId":"req-safe",
  "model":"thor",
  "result":{
    "label":"inbox",
    "spamProbability":0.2,
    "confidence":"high",
    "reasons":[],
    "flaggedTerms":[],
    "analyzedFields":["subject","body"],
    "modelVersion":"1",
    "analyzedAt":"2025-01-01T00:00:00Z"
  },
  "billing":{"chargedMillicents":1,"replayed":false}
}`

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func input() ClassificationInput {
	return ClassificationInput{Sender: "Example Team", Subject: "Subject", Body: "Body"}
}

func TestConsentDefaultsOffWithoutTransportCall(t *testing.T) {
	var calls atomic.Int32
	client, err := NewClient(Config{
		APIKey: "secret",
		transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return response(200, validJSON), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Classify(context.Background(), input(), false)
	assertKind(t, err, KindConsentRequired)
	if calls.Load() != 0 {
		t.Fatalf("transport called %d times", calls.Load())
	}
}

func TestExactRequestAndTypedResponse(t *testing.T) {
	var captured *http.Request
	var body string
	client, err := NewClient(Config{
		APIKey:              "top-secret",
		PaidAnalysisConsent: true,
		transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			captured = request
			raw, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			body = string(raw)
			return response(200, validJSON), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Classify(context.Background(), input(), false)
	if err != nil {
		t.Fatal(err)
	}
	if captured.URL.String() != "https://www.sendrepute.com/api/v1/classify" {
		t.Fatalf("unexpected URL %s", captured.URL)
	}
	if captured.Header.Get("Authorization") != "Bearer top-secret" {
		t.Fatal("missing authorization")
	}
	if body != `{"sender":"Example Team","subject":"Subject","body":"Body"}` {
		t.Fatalf("request transmitted unexpected fields: %s", body)
	}
	if result.RequestID != "req-safe" || result.Result.Label != "inbox" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEndpointRequiresHTTPSAndExplicitOrigin(t *testing.T) {
	for _, endpoint := range []string{
		"http://www.sendrepute.com/api",
		"https://user@www.sendrepute.com/api",
		"https://www.sendrepute.com/api?q=1",
		"https://evil.example/api",
		"http://localhost/api",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := NewClient(Config{APIKey: "secret", Endpoint: endpoint}); err == nil {
				t.Fatal("expected endpoint rejection")
			}
		})
	}
	client, err := NewClient(Config{
		APIKey:         "secret",
		Endpoint:       "https://classify.example.test/customer",
		AllowedOrigins: []string{"https://classify.example.test"},
		transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, validJSON), nil
		}),
	})
	if err != nil || client == nil {
		t.Fatalf("explicit trusted HTTPS endpoint rejected: %v", err)
	}
}

func TestRedirectRefusedAndNoRetry(t *testing.T) {
	var calls atomic.Int32
	client, err := NewClient(Config{
		APIKey: "secret", PaidAnalysisConsent: true,
		transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			result := response(302, `{}`)
			result.Header.Set("Location", "https://evil.example")
			return result, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Classify(context.Background(), input(), false)
	assertKind(t, err, KindRedirect)
	if calls.Load() != 1 {
		t.Fatalf("paid call was attempted %d times", calls.Load())
	}
}

func TestTransportFailureIsRedactedAndNotRetried(t *testing.T) {
	var calls atomic.Int32
	client, err := NewClient(Config{
		APIKey: "top-secret", PaidAnalysisConsent: true,
		transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("private network address")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	value := input()
	value.Body = "private body"
	_, err = client.Classify(context.Background(), value, false)
	assertKind(t, err, KindTransport)
	if calls.Load() != 1 {
		t.Fatalf("paid call was attempted %d times", calls.Load())
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("error leaked sensitive data: %v", err)
	}
}

func TestErrorsAreRedactedAndCategorized(t *testing.T) {
	private := "private body"
	for _, test := range []struct {
		status int
		kind   ErrorKind
	}{{401, KindAuthentication}, {402, KindBalance}, {429, KindRateLimit}} {
		client, err := NewClient(Config{
			APIKey: "top-secret", PaidAnalysisConsent: true,
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(test.status, `{"error":{"code":"SAFE_CODE","message":"`+private+`"},"requestId":"req-1"}`), nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		value := input()
		value.Body = private
		_, err = client.Classify(context.Background(), value, false)
		assertKind(t, err, test.kind)
		if strings.Contains(err.Error(), private) || strings.Contains(err.Error(), "top-secret") {
			t.Fatalf("error leaked sensitive data: %v", err)
		}
		var sdk *Error
		if !errors.As(err, &sdk) || sdk.RequestID != "req-1" || sdk.Code != "SAFE_CODE" {
			t.Fatalf("safe metadata missing: %#v", err)
		}
	}
}

func TestInvalidResponseShapeAndBounds(t *testing.T) {
	for _, body := range []string{
		strings.Replace(validJSON, `"spamProbability":0.2`, `"spamProbability":1.2`, 1),
		strings.Replace(validJSON, `"billing":`, `"unexpected":true,"billing":`, 1),
		strings.Replace(validJSON, `"replayed":false`, `"other":false`, 1),
		validJSON + `{}`,
	} {
		client, err := NewClient(Config{
			APIKey: "secret", PaidAnalysisConsent: true,
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(200, body), nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Classify(context.Background(), input(), false)
		assertKind(t, err, KindInvalidResponse)
	}
	large := strings.Repeat("x", maxResponseBytes+1)
	client, _ := NewClient(Config{
		APIKey: "secret", PaidAnalysisConsent: true,
		transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, large), nil
		}),
	})
	_, err := client.Classify(context.Background(), input(), false)
	assertKind(t, err, KindResponseTooLarge)
}

func TestNullResponsePrimitivesAreRejected(t *testing.T) {
	audit := `{
		"score":100,"grade":"A","summary":"looks_good",
		"counts":{"words":1,"links":0,"images":0,"triggerPhrases":0},
		"totalIssues":0,"criticalCount":0,"warningCount":0,"suggestionCount":0,
		"issues":[],"goodPractices":[],"inputTruncated":false
	}`
	reasonResponse := strings.Replace(validJSON, `"reasons":[]`, `"reasons":[{"signal":"x","detail":"y","weight":null}]`, 1)
	auditResponse := strings.Replace(validJSON, `"analyzedAt":"2025-01-01T00:00:00Z"`, `"analyzedAt":"2025-01-01T00:00:00Z","contentAudit":`+audit, 1)
	for name, body := range map[string]string{
		"billing charge": strings.Replace(validJSON, `"chargedMillicents":1`, `"chargedMillicents":null`, 1),
		"billing replay": strings.Replace(validJSON, `"replayed":false`, `"replayed":null`, 1),
		"probability":    strings.Replace(validJSON, `"spamProbability":0.2`, `"spamProbability":null`, 1),
		"reason weight":  reasonResponse,
		"string item":    strings.Replace(validJSON, `"flaggedTerms":[]`, `"flaggedTerms":[null]`, 1),
		"audit score":    strings.Replace(auditResponse, `"score":100`, `"score":null`, 1),
		"audit boolean":  strings.Replace(auditResponse, `"inputTruncated":false`, `"inputTruncated":null`, 1),
		"audit count":    strings.Replace(auditResponse, `"words":1`, `"words":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			client, err := NewClient(Config{
				APIKey: "secret", PaidAnalysisConsent: true,
				transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return response(200, body), nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Classify(context.Background(), input(), false)
			assertKind(t, err, KindInvalidResponse)
		})
	}
}

func TestBodyAndEncodedPayloadByteBounds(t *testing.T) {
	var calls atomic.Int32
	client, err := NewClient(Config{
		APIKey: "secret", PaidAnalysisConsent: true,
		transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return response(200, validJSON), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	value := input()
	value.Body = strings.Repeat("é", maxBodyBytes/2+1)
	_, err = client.Classify(context.Background(), value, false)
	assertKind(t, err, KindInvalidInput)

	value.Body = strings.Repeat(`"`, maxBodyBytes)
	_, err = client.Classify(context.Background(), value, false)
	assertKind(t, err, KindInvalidInput)
	if calls.Load() != 0 {
		t.Fatalf("oversized input reached transport %d times", calls.Load())
	}
}

func TestUnsafeErrorMetadataIsOmitted(t *testing.T) {
	client, err := NewClient(Config{
		APIKey: "secret", PaidAnalysisConsent: true,
		transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(401, `{"error":{"code":"LEAK secret\r\n"},"requestId":"bad id/control"}`), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Classify(context.Background(), input(), false)
	var sdk *Error
	if !errors.As(err, &sdk) {
		t.Fatal(err)
	}
	if sdk.Code != "API_ERROR" || sdk.RequestID != "" {
		t.Fatalf("unsafe metadata retained: %#v", sdk)
	}
}

func TestPrivateTransportIgnoresGlobalDefaultsAndVerifiesTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, validJSON)
	}))
	defer server.Close()

	original := http.DefaultTransport
	var globalCalls atomic.Int32
	globalUnsafe := server.Client().Transport
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		globalCalls.Add(1)
		return globalUnsafe.RoundTrip(request)
	})
	defer func() { http.DefaultTransport = original }()

	client, err := NewClient(Config{APIKey: "secret", PaidAnalysisConsent: true})
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(server.URL + "/api")
	if err != nil {
		t.Fatal(err)
	}
	client.endpoint = target
	_, err = client.Classify(context.Background(), input(), false)
	assertKind(t, err, KindTransport)
	if globalCalls.Load() != 0 {
		t.Fatalf("global transport hook was called %d times", globalCalls.Load())
	}
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify ||
		transport.DialTLSContext != nil {
		t.Fatalf("client transport is not privately TLS-safe: %#v", client.http.Transport)
	}
}

func TestRealTLSRedirectIsRefused(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "https://example.invalid/stolen", http.StatusFound)
	}))
	defer server.Close()
	client, err := NewClient(Config{
		APIKey: "secret", PaidAnalysisConsent: true,
		transport: server.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.endpoint, err = url.Parse(server.URL + "/api")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Classify(context.Background(), input(), false)
	assertKind(t, err, KindRedirect)
}

func TestDeadlineAndCallerCancellation(t *testing.T) {
	client, err := NewClient(Config{
		APIKey: "secret", PaidAnalysisConsent: true, Timeout: time.Millisecond,
		transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Classify(context.Background(), input(), false)
	assertKind(t, err, KindTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Classify(ctx, input(), false)
	assertKind(t, err, KindCancelled)
}

func TestConcurrentUse(t *testing.T) {
	client, err := NewClient(Config{
		APIKey: "secret", PaidAnalysisConsent: true,
		transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, validJSON), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := client.Classify(context.Background(), input(), false); err != nil {
				t.Errorf("concurrent classify: %v", err)
			}
		}()
	}
	wait.Wait()
}

func assertKind(t *testing.T, err error, kind ErrorKind) {
	t.Helper()
	var sdk *Error
	if !errors.As(err, &sdk) || sdk.Kind != kind {
		t.Fatalf("got %T %v, want kind %s", err, err, kind)
	}
}
