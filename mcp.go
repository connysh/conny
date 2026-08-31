package conny

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

const mcpPath = "/mcp"

const mcpInstructions = "Each tool calls one unary RPC on the upstream service. " +
	"A tool is named for its method's full proto name with dots replaced by " +
	"underscores, and takes that method's request message as JSON."

// Tool calls loop back through next — the transcoder — as Connect-unary JSON
// requests, so they reach the upstream the way any other client's would, over the
// configured protocol and through the same proxy.
func newMCPHandler(files *protoregistry.Files, next http.Handler, version string, logger *slog.Logger) (http.Handler, int) {
	if version == "" {
		version = "dev"
	}

	server := mcp.NewServer(
		&mcp.Implementation{Name: "conny", Version: version},
		&mcp.ServerOptions{
			Instructions: mcpInstructions,
			Logger:       logger,
			Capabilities: &mcp.ServerCapabilities{},
		},
	)

	tools := 0
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := range services.Len() {
			methods := services.Get(i).Methods()
			for j := range methods.Len() {
				md := methods.Get(j)
				if md.IsStreamingClient() || md.IsStreamingServer() {
					logger.Info("skipping streaming method", "method", md.FullName())
					continue
				}
				if name := toolName(md); len(name) > maxToolNameLength {
					logger.Warn("skipping method whose tool name is too long",
						"method", md.FullName(), "length", len(name), "limit", maxToolNameLength)
					continue
				}
				server.AddTool(newTool(md), toolHandler(md, next))
				tools++
			}
		}
		return true
	})
	if tools == 0 {
		logger.Warn("mcp enabled but no methods were exposed as tools")
	}

	// Stateless keeps every request self-contained: no session ids pinning a
	// client to one replica, and no initialize handshake before tools/list.
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: logger},
	)
	return handler, tools
}

const maxToolNameLength = 128

// toolName turns pay.v1.PaymentService.GetSession into
// pay_v1_PaymentService_GetSession. Qualifying it in full keeps names unique
// without resolving collisions.
func toolName(md protoreflect.MethodDescriptor) string {
	return strings.ReplaceAll(string(md.FullName()), ".", "_")
}

func newTool(md protoreflect.MethodDescriptor) *mcp.Tool {
	description := fmt.Sprintf("Calls the %s RPC.", md.FullName())
	if doc := comment(md); doc != "" {
		description = doc + "\n\n" + description
	}

	tool := &mcp.Tool{
		Name:        toolName(md),
		Description: description,
		InputSchema: requestSchema(md.Input()),
	}
	if isReadOnlyMethod(md) {
		tool.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
	}
	return tool
}

// isReadOnlyMethod reports whether md is bound to HTTP GET by a google.api.http
// annotation — the one machine-readable claim in a descriptor that a method only
// reads. Methods without the annotation are left unmarked rather than guessed at.
func isReadOnlyMethod(md protoreflect.MethodDescriptor) bool {
	options, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || options == nil {
		return false
	}
	rule, ok := proto.GetExtension(options, annotations.E_Http).(*annotations.HttpRule)
	if !ok || rule == nil {
		return false
	}
	_, isGet := rule.GetPattern().(*annotations.HttpRule_Get)
	return isGet
}

// toolHandler passes arguments through without reparsing, preserving protobuf
// JSON details such as 64-bit integers encoded as strings.
func toolHandler(md protoreflect.MethodDescriptor, next http.Handler) mcp.ToolHandler {
	path := fmt.Sprintf("/%s/%s", md.Parent().FullName(), md.Name())

	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body := []byte(req.Params.Arguments)
		if len(body) == 0 {
			body = []byte("{}")
		}

		r, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", "application/json")
		// Route by Connect method path rather than REST annotations.
		r.Header.Set("Connect-Protocol-Version", "1")
		if req.Extra != nil && req.Extra.Header != nil {
			if auth := req.Extra.Header.Get("Authorization"); auth != "" {
				r.Header.Set("Authorization", auth)
			}
		}

		w := &bufferedWriter{header: make(http.Header)}
		next.ServeHTTP(w, r)

		if w.status != http.StatusOK {
			return toolError(w), nil
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: w.body.String()}},
			StructuredContent: json.RawMessage(w.body.Bytes()),
		}, nil
	}
}

// toolError reports a failed RPC as a tool result rather than a JSON-RPC error.
func toolError(w *bufferedWriter) *mcp.CallToolResult {
	text := fmt.Sprintf("upstream returned %d %s", w.status, http.StatusText(w.status))

	var connectErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.body.Bytes(), &connectErr); err == nil && connectErr.Code != "" {
		text = connectErr.Code
		if connectErr.Message != "" {
			text += ": " + connectErr.Message
		}
	} else if body := strings.TrimSpace(w.body.String()); body != "" {
		text += ": " + body
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: true,
	}
}

// bufferedWriter collects a response in memory. It must implement http.Flusher:
// the transcoder refuses to serve a request whose writer cannot flush.
type bufferedWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *bufferedWriter) Header() http.Header { return w.header }

func (w *bufferedWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

func (w *bufferedWriter) Flush() {}
