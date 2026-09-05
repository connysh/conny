package conny

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The Machine Payments Protocol (MPP) is a challenge–credential–receipt flow over
// HTTP headers. gRPC/Connect have no 402, so an upstream signals the challenge as
// 401 Unauthenticated plus the header, and conny translates for clients that
// expect MPP's own shape: 402 for REST, and for MCP the binding in
// draft-payment-transport-mcp-00, where the challenge is a JSON-RPC error and the
// credential and receipt travel in _meta. Enabled by Config.MPP.

// ---- REST clients ----

// isPaymentChallenge reports whether wwwAuthenticate uses the "Payment" scheme.
func isPaymentChallenge(wwwAuthenticate string) bool {
	scheme := wwwAuthenticate
	if i := strings.IndexByte(scheme, ' '); i >= 0 {
		scheme = scheme[:i]
	}
	return strings.EqualFold(scheme, "Payment")
}

// withPaymentRequired upgrades REST 401 responses carrying a "Payment"
// challenge to 402. RPC requests pass through untouched: their clients read the
// code from the body/trailer, and rewriting the status would make the two disagree.
func withPaymentRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isRPCRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&paymentRequiredWriter{ResponseWriter: w}, r)
	})
}

// isRPCRequest reports whether the request arrived over gRPC, gRPC-Web, or
// Connect rather than as a plain REST/HTTP call. The Connect markers match
// vanguard's classifyRequest; they are the only thing distinguishing
// Connect-unary from REST, which share the application/json content type.
func isRPCRequest(r *http.Request) bool {
	if r.Header.Get("Connect-Protocol-Version") == "1" || r.URL.Query().Get("connect") == "v1" {
		return true
	}
	contentType := r.Header.Get("Content-Type")
	return strings.HasPrefix(contentType, "application/grpc") ||
		strings.HasPrefix(contentType, "application/connect")
}

type paymentRequiredWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *paymentRequiredWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if code == http.StatusUnauthorized &&
			isPaymentChallenge(w.Header().Get("Www-Authenticate")) {
			code = http.StatusPaymentRequired
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *paymentRequiredWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush and Unwrap expose the underlying writer's capabilities; the transcoder
// requires the response writer to implement http.Flusher.
func (w *paymentRequiredWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *paymentRequiredWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// ---- MCP clients ----

const (
	// JSON-RPC error codes the binding assigns, inside the server-defined range.
	mppPaymentRequiredCode    = -32042
	mppPaymentRequiredMessage = "Payment Required"
	mppVerificationFailedCode = -32043
	mppVerificationFailedMsg  = "Payment Verification Failed"
	mppMalformedCredentialMsg = "Malformed credential"

	// _meta keys the binding reserves.
	mppCredentialMeta = "org.paymentauth/credential"
	mppReceiptMeta    = "org.paymentauth/receipt"

	mppReceiptHeader = "Payment-Receipt"
)

// paymentCredential is a _meta credential in the header form the upstream verifies.
type paymentCredential struct {
	header  string // field the challenge asked the credential to travel in
	value   string // "Payment " + base64url credential
	expires time.Time
}

// parsePaymentCredential returns nil, nil when meta carries no credential, and an
// invalid-params error for one the binding would reject, before anything goes upstream.
func parsePaymentCredential(meta mcp.Meta) (*paymentCredential, error) {
	raw, ok := meta[mppCredentialMeta]
	if !ok || raw == nil {
		return nil, nil
	}

	credential, ok := raw.(map[string]any)
	if !ok {
		return nil, malformedCredential("must be an object")
	}
	challenge, ok := credential["challenge"].(map[string]any)
	if !ok {
		return nil, malformedCredential(`"challenge" must be an object`)
	}
	if id, _ := challenge["id"].(string); id == "" {
		return nil, malformedCredential(`"challenge.id" must be a non-empty string`)
	}
	if _, ok := credential["payload"].(map[string]any); !ok {
		return nil, malformedCredential(`"payload" must be an object`)
	}

	// The client echoes request as the object it received; the upstream expects
	// the base64url JCS string it issued. Sorted keys and plain escaping
	// reproduce JCS for the flat, string-valued requests MPP methods use.
	if request, ok := challenge["request"]; ok {
		if _, isString := request.(string); !isString {
			encoded, err := canonicalJSON(request)
			if err != nil {
				return nil, malformedCredential(`"challenge.request" is not encodable`)
			}
			challenge = cloneMap(challenge)
			challenge["request"] = base64.RawURLEncoding.EncodeToString(encoded)
			credential = cloneMap(credential)
			credential["challenge"] = challenge
		}
	}

	encoded, err := canonicalJSON(credential)
	if err != nil {
		return nil, malformedCredential("not encodable")
	}
	c := &paymentCredential{
		header: http.CanonicalHeaderKey("Authorization"),
		value:  "Payment " + base64.RawURLEncoding.EncodeToString(encoded),
	}
	if name, _ := challenge["header"].(string); name != "" {
		c.header = http.CanonicalHeaderKey(name)
	}
	if expires, _ := challenge["expires"].(string); expires != "" {
		c.expires, _ = time.Parse(time.RFC3339, expires)
	}
	return c, nil
}

func malformedCredential(detail string) error {
	return &jsonrpc.Error{
		Code:    jsonrpc.CodeInvalidParams,
		Message: fmt.Sprintf("%s: %s %s", mppMalformedCredentialMsg, mppCredentialMeta, detail),
	}
}

// mppChallengeError builds the JSON-RPC error for a response carrying Payment
// challenges (one per WWW-Authenticate header), or nil if it carries none. A
// challenge answering a credential means it was rejected, which gets its own
// code so the client re-pays rather than resubmitting.
func mppChallengeError(w *bufferedWriter, credential *paymentCredential) error {
	var challenges []map[string]any
	for _, value := range w.header.Values("Www-Authenticate") {
		if challenge, ok := parsePaymentChallenge(value); ok {
			challenges = append(challenges, challenge)
		}
	}
	if len(challenges) == 0 {
		return nil
	}

	// The binding wants 402 here even though a Connect upstream answered 401.
	data := map[string]any{
		"httpStatus": http.StatusPaymentRequired,
		"challenges": challenges,
	}
	code, message := mppPaymentRequiredCode, mppPaymentRequiredMessage
	if credential != nil {
		code, message = mppVerificationFailedCode, mppVerificationFailedMsg
		data["failure"] = paymentFailure(w, credential)
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return &jsonrpc.Error{Code: int64(code), Message: message, Data: encoded}
}

// paymentFailure explains a rejected credential from the best evidence available:
// an RFC 9457 problem body, the challenge's expiry, the Connect code, else generic.
func paymentFailure(w *bufferedWriter, credential *paymentCredential) map[string]any {
	failure := map[string]any{"reason": "verification-failed"}

	var body struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		// Connect error fields.
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(w.body.Bytes(), &body)

	switch {
	case body.Type != "" && body.Type != "about:blank":
		failure["reason"] = body.Type[strings.LastIndexAny(body.Type, "/:#")+1:]
	case !credential.expires.IsZero() && credential.expires.Before(time.Now()):
		failure["reason"] = "payment-expired"
	case body.Code == "invalid_argument":
		failure["reason"] = "malformed-credential"
	}
	if detail := firstNonEmpty(body.Detail, body.Title, body.Message); detail != "" {
		failure["detail"] = detail
	}
	return failure
}

// paymentReceipt decodes a Payment-Receipt header into _meta form, or nil. An
// undecodable receipt is dropped rather than failing a call already charged for.
func paymentReceipt(header http.Header) mcp.Meta {
	raw := header.Get(mppReceiptHeader)
	if raw == "" {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "="))
	if err != nil {
		return nil
	}
	var receipt map[string]any
	if json.Unmarshal(decoded, &receipt) != nil || receipt == nil {
		return nil
	}
	return mcp.Meta{mppReceiptMeta: receipt}
}

// parsePaymentChallenge converts one "Payment ..." challenge to the binding's JSON
// form. Parameters pass through verbatim, since the upstream checks the client's
// echo against what it issued; only request is decoded, as the binding requires.
func parsePaymentChallenge(value string) (map[string]any, bool) {
	if !isPaymentChallenge(value) {
		return nil, false
	}
	params, ok := parseAuthParams(strings.TrimLeft(value[len("Payment"):], " \t"))
	if !ok {
		return nil, false
	}
	for _, name := range []string{"id", "realm", "method", "intent", "request"} {
		if params[name] == "" {
			return nil, false
		}
	}

	request, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(params["request"], "="))
	if err != nil || !json.Valid(request) {
		return nil, false
	}

	challenge := make(map[string]any, len(params))
	for name, v := range params {
		challenge[name] = v
	}
	challenge["request"] = json.RawMessage(request)
	return challenge, true
}

// parseAuthParams reads RFC 9110 auth-params: name=value pairs separated by
// commas, where a value is a token or a quoted-string with backslash escapes.
// Names are lowercased, as the grammar treats them case-insensitively.
func parseAuthParams(s string) (map[string]string, bool) {
	params := map[string]string{}
	for {
		s = strings.TrimLeft(s, " \t,")
		if s == "" {
			return params, true
		}

		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return nil, false
		}
		name := strings.ToLower(strings.TrimRight(s[:eq], " \t"))
		if name == "" || strings.ContainsAny(name, " \t,\"") {
			return nil, false
		}
		s = strings.TrimLeft(s[eq+1:], " \t")

		var value string
		if strings.HasPrefix(s, `"`) {
			var sb strings.Builder
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				sb.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, false // unterminated quoted-string
			}
			value, s = sb.String(), s[i+1:]
		} else {
			end := strings.IndexAny(s, " \t,")
			if end < 0 {
				end = len(s)
			}
			value, s = s[:end], s[end:]
		}
		params[name] = value
	}
}

// canonicalJSON encodes v compactly with sorted keys and no HTML escaping.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
