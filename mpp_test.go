package conny

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestIsPaymentChallenge(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{"Payment", true},
		{"payment", true},
		{"Payment realm=\"api\", amount=100", true},
		{"Bearer realm=\"api\"", false},
		{"Basic", false},
		{"", false},
		{"PaymentX", false},
	}
	for _, tt := range tests {
		if got := isPaymentChallenge(tt.header); got != tt.want {
			t.Errorf("isPaymentChallenge(%q) = %v, want %v", tt.header, got, tt.want)
		}
	}
}

func TestIsRPCRequest(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		connectHdr  string
		query       string
		want        bool
	}{
		{"rest json", "application/json", "", "", false},
		{"rest no content type", "", "", "", false},
		{"grpc", "application/grpc", "", "", true},
		{"grpc proto", "application/grpc+proto", "", "", true},
		{"grpc-web", "application/grpc-web+proto", "", "", true},
		{"connect streaming", "application/connect+json", "", "", true},
		{"connect unary header", "application/json", "1", "", true},
		{"connect get query", "", "", "connect=v1", true},
		{"wrong connect version", "application/json", "2", "", false},
		{"wrong connect query", "", "", "connect=v2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/svc/Method?"+tt.query, nil)
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			if tt.connectHdr != "" {
				r.Header.Set("Connect-Protocol-Version", tt.connectHdr)
			}
			if got := isRPCRequest(r); got != tt.want {
				t.Errorf("isRPCRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWithPaymentRequired(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wwwAuth    string
		rpcRequest bool
		wantStatus int
	}{
		{"401 payment upgraded", http.StatusUnauthorized, "Payment realm=\"api\"", false, http.StatusPaymentRequired},
		{"401 bearer untouched", http.StatusUnauthorized, "Bearer realm=\"api\"", false, http.StatusUnauthorized},
		{"401 no challenge untouched", http.StatusUnauthorized, "", false, http.StatusUnauthorized},
		{"403 payment untouched", http.StatusForbidden, "Payment", false, http.StatusForbidden},
		{"200 untouched", http.StatusOK, "", false, http.StatusOK},
		{"rpc request untouched", http.StatusUnauthorized, "Payment", true, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.wwwAuth != "" {
					w.Header().Set("Www-Authenticate", tt.wwwAuth)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("body"))
			})
			r := httptest.NewRequest(http.MethodGet, "/svc/Method", nil)
			if tt.rpcRequest {
				r.Header.Set("Connect-Protocol-Version", "1")
			}
			w := httptest.NewRecorder()

			withPaymentRequired(next).ServeHTTP(w, r)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Body.String(); got != "body" {
				t.Errorf("body = %q, want %q", got, "body")
			}
			if tt.wwwAuth != "" && w.Header().Get("Www-Authenticate") != tt.wwwAuth {
				t.Errorf("Www-Authenticate header not preserved")
			}
		})
	}
}

func TestPaymentRequiredWriterImplicitHeader(t *testing.T) {
	// Write without an explicit WriteHeader must default to 200.
	rec := httptest.NewRecorder()
	w := &paymentRequiredWriter{ResponseWriter: rec}
	if _, err := w.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestPaymentRequiredWriterOnlyFirstHeaderRewritten(t *testing.T) {
	// A second WriteHeader call (invalid, but defensive) must not re-evaluate.
	rec := httptest.NewRecorder()
	w := &paymentRequiredWriter{ResponseWriter: rec}
	w.Header().Set("Www-Authenticate", "Payment")
	w.WriteHeader(http.StatusOK)
	w.WriteHeader(http.StatusUnauthorized)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// The example challenge from the MPP specification, with request carrying
// {"amount":"1000","currency":"usd"} and opaque carrying {"route":"/v1/search"}.
const specChallenge = `Payment id="qB3wErTyU7iOpAsD9fGhJk", realm="mpp.dev", method="tempo", ` +
	`intent="charge", expires="2025-01-15T12:05:00Z", header="Payment-Authorization", ` +
	`opaque="eyJyb3V0ZSI6Ii92MS9zZWFyY2gifQ", request="eyJhbW91bnQiOiIxMDAwIiwiY3VycmVuY3kiOiJ1c2QifQ"`

func TestParsePaymentChallenge(t *testing.T) {
	challenge, ok := parsePaymentChallenge(specChallenge)
	if !ok {
		t.Fatal("parsePaymentChallenge rejected the specification's example")
	}

	want := map[string]any{
		"id":      "qB3wErTyU7iOpAsD9fGhJk",
		"realm":   "mpp.dev",
		"method":  "tempo",
		"intent":  "charge",
		"expires": "2025-01-15T12:05:00Z",
		"header":  "Payment-Authorization",
		"opaque":  "eyJyb3V0ZSI6Ii92MS9zZWFyY2gifQ",
		"request": json.RawMessage(`{"amount":"1000","currency":"usd"}`),
	}
	if !reflect.DeepEqual(challenge, want) {
		t.Errorf("challenge = %#v\nwant %#v", challenge, want)
	}
}

func TestParsePaymentChallengeSyntax(t *testing.T) {
	tests := []struct {
		name  string
		value string
		ok    bool
		check func(map[string]any) bool
	}{
		{
			name:  "unquoted tokens and padded base64",
			value: `Payment id=abc, realm=api, method=tempo, intent=charge, request=e30=`,
			ok:    true,
			check: func(c map[string]any) bool { return string(c["request"].(json.RawMessage)) == "{}" },
		},
		{
			name:  "escaped quote and spaces around equals",
			value: `payment id = "a\"b", realm="r", method="m", intent="i", request="e30"`,
			ok:    true,
			check: func(c map[string]any) bool { return c["id"] == `a"b` },
		},
		{
			name:  "parameter names are case-insensitive",
			value: `Payment ID="a", Realm="r", METHOD="m", intent="i", Request="e30"`,
			ok:    true,
			check: func(c map[string]any) bool { return c["id"] == "a" && c["method"] == "m" },
		},
		{name: "other scheme", value: `Bearer realm="api"`},
		{name: "missing intent", value: `Payment id="a", realm="r", method="m", request="e30"`},
		{name: "request is not base64url", value: `Payment id="a", realm="r", method="m", intent="i", request="not base64!"`},
		{name: "request is not json", value: `Payment id="a", realm="r", method="m", intent="i", request="aGVsbG8"`},
		{name: "unterminated quoted string", value: `Payment id="a, realm="r", method="m", intent="i", request="e30"`},
		{name: "parameter without value", value: `Payment id, realm="r", method="m", intent="i", request="e30"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			challenge, ok := parsePaymentChallenge(tt.value)
			if ok != tt.ok {
				t.Fatalf("ok = %t, want %t (challenge %v)", ok, tt.ok, challenge)
			}
			if tt.check != nil && !tt.check(challenge) {
				t.Errorf("unexpected challenge %v", challenge)
			}
		})
	}
}

func TestMCPToolCallPaymentChallenge(t *testing.T) {
	handler, upstream := mcpTestHandlerWithMPP(t, true)
	upstream.status = http.StatusUnauthorized
	upstream.body = `{"code":"unauthenticated","message":"payment required"}`
	upstream.header = http.Header{"Www-Authenticate": {
		specChallenge,
		`Payment id="def", realm="mpp.dev", method="stripe", intent="charge", request="e30"`,
		`Bearer realm="mpp.dev"`,
	}}

	result, rpcErr := callMCPRaw(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"test_v1_TestService_GetThing","arguments":{}}}`)
	if rpcErr == nil {
		t.Fatalf("got a tool result, want a JSON-RPC error: %s", result)
	}

	var got struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			HTTPStatus int               `json:"httpStatus"`
			Challenges []json.RawMessage `json:"challenges"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rpcErr, &got); err != nil {
		t.Fatalf("decoding %s: %v", rpcErr, err)
	}
	if got.Code != mppPaymentRequiredCode || got.Message != mppPaymentRequiredMessage {
		t.Errorf("error = %d %q, want %d %q", got.Code, got.Message, mppPaymentRequiredCode, mppPaymentRequiredMessage)
	}
	if got.Data.HTTPStatus != http.StatusPaymentRequired {
		t.Errorf("httpStatus = %d, want 402", got.Data.HTTPStatus)
	}
	if len(got.Data.Challenges) != 2 {
		t.Fatalf("challenges = %s, want the two Payment challenges", rpcErr)
	}

	var first struct {
		ID      string `json:"id"`
		Method  string `json:"method"`
		Request struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"request"`
	}
	if err := json.Unmarshal(got.Data.Challenges[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.ID != "qB3wErTyU7iOpAsD9fGhJk" || first.Method != "tempo" ||
		first.Request.Amount != "1000" || first.Request.Currency != "usd" {
		t.Errorf("first challenge = %s, want the decoded spec example", got.Data.Challenges[0])
	}
}

func TestMCPToolCallPaymentChallengeFallsBackToToolError(t *testing.T) {
	tests := []struct {
		name   string
		mpp    bool
		header string
	}{
		{"payment disabled", false, specChallenge},
		{"malformed challenge", true, `Payment id="a"`},
		{"no challenge", true, `Bearer realm="api"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, upstream := mcpTestHandlerWithMPP(t, tt.mpp)
			upstream.status = http.StatusUnauthorized
			upstream.body = `{"code":"unauthenticated","message":"payment required"}`
			upstream.header = http.Header{"Www-Authenticate": {tt.header}}

			result := callMCP(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
				`{"name":"test_v1_TestService_GetThing","arguments":{}}}`)

			var call struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			if err := json.Unmarshal(result, &call); err != nil {
				t.Fatal(err)
			}
			if !call.IsError || len(call.Content) != 1 || call.Content[0].Text != "unauthenticated: payment required" {
				t.Errorf("result = %s, want the plain tool error", result)
			}
		})
	}
}

// A credential as an MCP client sends it: the spec example challenge echoed with
// request decoded, plus a method-specific payload.
const testCredential = `{"challenge":{"id":"qB3wErTyU7iOpAsD9fGhJk","realm":"mpp.dev","method":"tempo",` +
	`"intent":"charge","expires":"2099-01-15T12:05:00Z","opaque":"eyJyb3V0ZSI6Ii92MS9zZWFyY2gifQ",` +
	`"request":{"currency":"usd","amount":"1000"}},"source":"0x1234","payload":{"signature":"0xabc"}}`

func toolCallWithMeta(meta string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` +
		`{"name":"test_v1_TestService_GetThing","arguments":{},"_meta":` + meta + `}}`
}

func TestMCPToolCallSendsCredential(t *testing.T) {
	handler, upstream := mcpTestHandlerWithMPP(t, true)
	upstream.header = http.Header{mppReceiptHeader: {"eyJzdGF0dXMiOiJzdWNjZXNzIiwibWV0aG9kIjoidGVtcG8ifQ=="}}

	result := callMCP(t, handler, toolCallWithMeta(`{"org.paymentauth/credential":`+testCredential+`}`))

	auth := upstream.request.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Payment ") {
		t.Fatalf("Authorization = %q, want a Payment credential", auth)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(auth, "Payment "))
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Challenge map[string]any `json:"challenge"`
		Source    string         `json:"source"`
		Payload   map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(decoded, &sent); err != nil {
		t.Fatalf("credential is not JSON: %s", decoded)
	}
	if sent.Source != "0x1234" || sent.Payload["signature"] != "0xabc" || sent.Challenge["id"] != "qB3wErTyU7iOpAsD9fGhJk" {
		t.Errorf("credential = %s, want the client's fields passed through", decoded)
	}
	// request goes back to the base64url JCS string the upstream issued.
	if got, want := sent.Challenge["request"], "eyJhbW91bnQiOiIxMDAwIiwiY3VycmVuY3kiOiJ1c2QifQ"; got != want {
		t.Errorf("challenge.request = %v, want %q", got, want)
	}

	var call struct {
		Meta map[string]map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(result, &call); err != nil {
		t.Fatal(err)
	}
	if receipt := call.Meta[mppReceiptMeta]; receipt["status"] != "success" || receipt["method"] != "tempo" {
		t.Errorf("_meta = %v, want the decoded receipt", call.Meta)
	}
}

func TestMCPToolCallCredentialHeaderParameter(t *testing.T) {
	handler, upstream := mcpTestHandlerWithMPP(t, true)
	credential := strings.Replace(testCredential, `"realm":"mpp.dev",`, `"realm":"mpp.dev","header":"Payment-Authorization",`, 1)

	callMCP(t, handler, toolCallWithMeta(`{"org.paymentauth/credential":`+credential+`}`))

	if got := upstream.request.Header.Get("Payment-Authorization"); !strings.HasPrefix(got, "Payment ") {
		t.Errorf("Payment-Authorization = %q, want the credential there, as the challenge asked", got)
	}
	if got := upstream.request.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty", got)
	}
}

func TestMCPToolCallCredentialIgnoredWithoutPayment(t *testing.T) {
	handler, upstream := mcpTestHandlerWithMPP(t, false)

	callMCP(t, handler, toolCallWithMeta(`{"org.paymentauth/credential":`+testCredential+`}`))

	if got := upstream.request.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none when payment is off", got)
	}
}

func TestMCPToolCallMalformedCredential(t *testing.T) {
	tests := []struct {
		name string
		meta string
	}{
		{"not an object", `{"org.paymentauth/credential":"abc"}`},
		{"no challenge", `{"org.paymentauth/credential":{"payload":{}}}`},
		{"challenge without id", `{"org.paymentauth/credential":{"challenge":{"realm":"r"},"payload":{}}}`},
		{"no payload", `{"org.paymentauth/credential":{"challenge":{"id":"a"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, upstream := mcpTestHandlerWithMPP(t, true)

			_, rpcErr := callMCPRaw(t, handler, toolCallWithMeta(tt.meta))
			if rpcErr == nil {
				t.Fatal("got a result, want an invalid-params error")
			}
			var got struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(rpcErr, &got); err != nil {
				t.Fatal(err)
			}
			if got.Code != -32602 || !strings.HasPrefix(got.Message, mppMalformedCredentialMsg) {
				t.Errorf("error = %d %q, want -32602 %q...", got.Code, got.Message, mppMalformedCredentialMsg)
			}
			if upstream.request != nil {
				t.Error("upstream was called, want the credential rejected first")
			}
		})
	}
}

func TestMCPToolCallVerificationFailed(t *testing.T) {
	tests := []struct {
		name       string
		credential string
		body       string
		wantReason string
		wantDetail string
	}{
		{
			name:       "connect error",
			credential: testCredential,
			body:       `{"code":"unauthenticated","message":"bad signature"}`,
			wantReason: "verification-failed",
			wantDetail: "bad signature",
		},
		{
			name:       "problem details",
			credential: testCredential,
			body:       `{"type":"https://mpp.dev/problems/payment-insufficient","title":"Amount too low","detail":"need 1000"}`,
			wantReason: "payment-insufficient",
			wantDetail: "need 1000",
		},
		{
			name:       "expired challenge",
			credential: strings.Replace(testCredential, "2099-01-15", "2020-01-15", 1),
			body:       `{"code":"unauthenticated"}`,
			wantReason: "payment-expired",
		},
		{
			name:       "invalid argument",
			credential: testCredential,
			body:       `{"code":"invalid_argument","message":"cannot parse credential"}`,
			wantReason: "malformed-credential",
			wantDetail: "cannot parse credential",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, upstream := mcpTestHandlerWithMPP(t, true)
			upstream.status = http.StatusUnauthorized
			upstream.body = tt.body
			upstream.header = http.Header{"Www-Authenticate": {specChallenge}}

			_, rpcErr := callMCPRaw(t, handler, toolCallWithMeta(`{"org.paymentauth/credential":`+tt.credential+`}`))
			if rpcErr == nil {
				t.Fatal("got a result, want a verification-failed error")
			}
			var got struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Data    struct {
					HTTPStatus int               `json:"httpStatus"`
					Challenges []json.RawMessage `json:"challenges"`
					Failure    map[string]string `json:"failure"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rpcErr, &got); err != nil {
				t.Fatal(err)
			}
			if got.Code != mppVerificationFailedCode || got.Message != mppVerificationFailedMsg {
				t.Errorf("error = %d %q, want %d %q", got.Code, got.Message, mppVerificationFailedCode, mppVerificationFailedMsg)
			}
			if got.Data.HTTPStatus != 402 || len(got.Data.Challenges) != 1 {
				t.Errorf("data = %s, want 402 and one fresh challenge", rpcErr)
			}
			if got.Data.Failure["reason"] != tt.wantReason || got.Data.Failure["detail"] != tt.wantDetail {
				t.Errorf("failure = %v, want reason %q detail %q", got.Data.Failure, tt.wantReason, tt.wantDetail)
			}
		})
	}
}

func TestMCPToolCallCredentialDeniedByPolicy(t *testing.T) {
	// 403 with no challenge: payment was fine, access was not. A plain tool
	// error, not a payment error.
	handler, upstream := mcpTestHandlerWithMPP(t, true)
	upstream.status = http.StatusForbidden
	upstream.body = `{"code":"permission_denied","message":"region blocked"}`

	result := callMCP(t, handler, toolCallWithMeta(`{"org.paymentauth/credential":`+testCredential+`}`))
	if !strings.Contains(string(result), `"isError":true`) || !strings.Contains(string(result), "permission_denied: region blocked") {
		t.Errorf("result = %s, want the plain tool error", result)
	}
}

func TestPaymentReceiptUndecodableIsDropped(t *testing.T) {
	for _, raw := range []string{"not base64!", "aGVsbG8", "W10"} {
		if got := paymentReceipt(http.Header{mppReceiptHeader: {raw}}); got != nil {
			t.Errorf("paymentReceipt(%q) = %v, want nil", raw, got)
		}
	}
}
