# SendRepute Go integration

An installable, standard-library-only, server-side Go client for the paid
SendRepute customer classification endpoint.

## Installation

```sh
go get github.com/sendrepute/sendrepute-go@main
```

For source builds and offline tests:

```sh
git clone https://github.com/sendrepute/sendrepute-go.git
cd sendrepute-go
go test ./...
go test -race ./...
```

The runtime and packaging tool use only the Go standard library. Go 1.22 or
newer is required.

## Usage

Keep the bearer credential only in server secret storage. Never embed it in a
browser/mobile binary, send it to a browser, or log it.

```go
client, err := sendrepute.NewClient(sendrepute.Config{
    APIKey: os.Getenv("SENDREPUTE_API_KEY"),
    PaidAnalysisConsent: true, // explicit opt-in: each fresh input may be billed
})
if err != nil {
    return err
}

result, err := client.Classify(ctx, sendrepute.ClassificationInput{
    Sender: "Example Team",
    Subject: subject,
    Body: body,
    Model: sendrepute.ModelThor, // optional; omit to use the account default
}, false)
```

Consent defaults to disabled. Alternatively, leave client consent disabled and
pass `true` as the final `Classify` argument only at a call site that has
obtained explicit paid consent.

The canonical endpoint is fixed by default to
`https://www.sendrepute.com/api/v1/classify`. A deployment-specific endpoint
must use verified HTTPS and its origin must be explicitly listed in
`AllowedOrigins`. Loopback endpoints are deliberately not trusted in production.
Tests primarily inject an in-memory `http.RoundTripper`, with additional
offline loopback TLS-handshake tests; they make no external or paid calls.

The client sends only sender display name, subject, body, and an optional model.
It has no recipient or attachment input, refuses redirects, verifies TLS with
the system roots using a private transport (not mutable global HTTP defaults),
bounds context duration, the body to 524,288 UTF-8 bytes, encoded JSON requests
to 768 KiB, and responses to 1 MiB, and performs **no retries**. One `Classify`
invocation makes at most one paid HTTP attempt.

Responses are decoded into typed values and strictly checked for required and
unknown fields, JSON primitive types (including rejection of `null`), finite
probability/weights, ranges, enums, strings, and content-audit bounds. Errors
contain only a safe category, restricted identifier-form server code/request ID,
and HTTP status; credentials, message content, response bodies, and network
details are never included.

## Billing, privacy, and application policy

Classification is a content-safety signal, not a guarantee of deliverability or
inbox placement. An identical request may replay a server receipt; changed
sender, subject, body, or model can be a new paid analysis. The SDK does not
make send/block decisions. Thresholds and exact failure behavior (allow, defer,
or fail closed) belong to the host application and must be selected and tested
there, especially for password resets and other critical transactional mail.

The SDK does not render templates, inspect MIME messages, choose HTML/text
alternatives, transmit recipients or attachments, queue work, retry failures,
or send email. It supports classification only.

## ZIP packaging

Create the distributable source archive with the allowlist-only packager:

```sh
go run ./scripts/package.go
```

It writes `dist/sendrepute-go-v0.1.0.zip`, rejects symlinks and
non-regular source files, and includes only the module files explicitly listed
in the packaging source. The ZIP is for local distribution; it does not publish
the GitHub repository.

## Support and security

Report non-sensitive bugs through GitHub issues. Account support and private
security reports: support@sendrepute.com. Do not attach credentials or customer
messages. See [SECURITY.md](SECURITY.md). Licensed under MIT; see LICENSE.