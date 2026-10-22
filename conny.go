// Package conny is a tiny ConnectRPC gateway. It transcodes REST/JSON, gRPC,
// gRPC-Web, and Connect requests onto an upstream RPC service described by a
// proto descriptor set.
//
// It can be used as a library by building a [Config] and calling [NewHandler]
// to obtain an http.Handler, or [ListenAndServe] to run it directly. The conny
// command (cmd/conny) is a thin CLI wrapper around this package.
package conny

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Config describes a conny gateway. A descriptor (Descriptor or
// DescriptorPath) and a Target are required.
type Config struct {
	// Descriptor is the proto FileDescriptorSet describing the upstream
	// services. Takes precedence over DescriptorPath.
	Descriptor *descriptorpb.FileDescriptorSet

	// DescriptorPath is the path to a serialized FileDescriptorSet.
	DescriptorPath string

	// Target is the upstream base URL. A "h2c://" scheme enables HTTP/2 over
	// cleartext to the upstream (otherwise it is treated as "http://").
	Target string

	// Protocol is the upstream RPC protocol: "connect", "grpc", or "grpcweb"
	// ("grpc-web" is also accepted). Defaults to "connect" when empty.
	Protocol string

	// Reflection enables gRPC server reflection (v1 and v1alpha).
	Reflection bool

	// StaticDir is a directory served alongside the RPC routes — for a
	// pre-generated openapi.json, a docs UI, and the like. Requests that do not
	// name a file in it fall through to the RPC routes. Disabled when empty.
	StaticDir string

	// MCP serves a Model Context Protocol endpoint at /mcp, exposing each unary
	// method in the descriptor as a tool an agent can call. Disabled when false.
	MCP bool

	// MPP translates the upstream's Machine Payments Protocol flow for clients
	// that expect MPP's native shape: 402 Payment Required for REST, and the
	// MPP MCP binding (JSON-RPC errors and _meta) for MCP tool calls.
	MPP bool

	// Version is reported to MCP clients as the server's version. Defaults to
	// "dev" when empty.
	Version string

	// Logger receives structured logs. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

func (c Config) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// NewHandler builds the gateway's http.Handler — /health, any static files, an
// MCP endpoint at /mcp when enabled, plus the RPC and REST routes —
// for mounting in your own server or middleware stack. To accept gRPC or h2c
// clients, enable unencrypted HTTP/2 on your server (see [ListenAndServe]).
func NewHandler(c Config) (http.Handler, error) {
	logger := c.logger()

	fds, err := c.resolveDescriptor()
	if err != nil {
		return nil, err
	}
	logger.Info("loaded descriptor set", "files", len(fds.GetFile()))

	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, fmt.Errorf("conny: creating file registry: %w", err)
	}

	target, enableH2C, err := c.resolveTarget()
	if err != nil {
		return nil, err
	}

	protocol, err := parseProtocol(c.Protocol)
	if err != nil {
		return nil, err
	}

	types := dynamicpb.NewTypes(files)
	upstream := newUpstreamClient(target, protocol, types, enableH2C, logger)
	server, services, err := newServer(files, types, upstream, c.Reflection, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("registered services", "count", services)

	rpc, err := newRPCHandler(server, types)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("ok\n"))
		}
	})

	if c.MPP {
		logger.Info("translating mpp payment flow for rest and mcp clients")
	}

	if c.MCP {
		mcpHandler, tools := newMCPHandler(files, rpc, c.Version, c.MPP, logger)
		mux.Handle(mcpPath, mcpHandler)
		logger.Info("serving mcp", "path", mcpPath, "tools", tools)
	}

	rootHandler := rpc
	if c.MPP {
		rootHandler = withPaymentRequired(rpc)
	}
	if c.StaticDir != "" {
		rootHandler, err = withStatic(c.StaticDir, rootHandler)
		if err != nil {
			return nil, err
		}
		logger.Info("serving static files", "dir", c.StaticDir)
	}
	mux.Handle("/", rootHandler)

	return mux, nil
}

// ListenAndServe builds the gateway handler and serves it on addr over HTTP/1
// and h2c (required for gRPC clients), blocking until the server stops. For
// custom server configuration, use [NewHandler] with your own http.Server.
func ListenAndServe(addr string, c Config) error {
	handler, err := NewHandler(c)
	if err != nil {
		return err
	}

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:      addr,
		Handler:   handler,
		Protocols: protocols,
	}
	return server.ListenAndServe()
}

func (c Config) resolveDescriptor() (*descriptorpb.FileDescriptorSet, error) {
	if c.Descriptor != nil {
		return c.Descriptor, nil
	}
	if c.DescriptorPath == "" {
		return nil, errors.New("conny: Descriptor or DescriptorPath is required")
	}
	return loadDescriptorSet(c.DescriptorPath)
}

func (c Config) resolveTarget() (*url.URL, bool, error) {
	raw := c.Target
	if raw == "" {
		return nil, false, errors.New("conny: Target is required")
	}
	enableH2C := false
	if strings.HasPrefix(raw, "h2c://") {
		enableH2C = true
		raw = "http://" + strings.TrimPrefix(raw, "h2c://")
	}
	target, err := url.Parse(raw)
	if err != nil {
		return nil, false, fmt.Errorf("conny: invalid target URL: %w", err)
	}
	return target, enableH2C, nil
}

// newRPCHandler serves server over Connect, gRPC, and gRPC-Web, and over REST
// for methods with google.api.http annotations. RPC messages keep connecthttp's
// per-message read limit. REST bodies are uncapped, because vanguard limits a
// whole body, not a chunk, and a cap would break streaming HttpBody uploads.
func newRPCHandler(server *connect.Server, types *dynamicpb.Types) (http.Handler, error) {
	mux := http.NewServeMux()

	jsonCodec := connectproto.NewJSONCodec(connectproto.WithTypeResolver(types))
	jsonCodec.UnmarshalOptions.DiscardUnknown = true
	connecthttp.Mount(mux, server,
		connecthttp.WithCodecs(connectproto.NewBinaryCodec(connectproto.WithTypeResolver(types)), jsonCodec),
	)

	err := vanguard.Mount(mux, server,
		vanguard.WithTypeResolver(types),
		vanguard.WithCodec(func(res connectproto.TypeResolver) vanguard.RESTCodec {
			codec := vanguard.NewJSONCodec(res)
			codec.MarshalOptions.UseProtoNames = true
			codec.MarshalOptions.EmitUnpopulated = true
			codec.UnmarshalOptions.DiscardUnknown = true
			return codec
		}),
		vanguard.WithDiscardUnknownQueryParams(true),
		vanguard.WithMaxReadBytes(0),
	)
	if err != nil {
		return nil, fmt.Errorf("conny: mounting rest routes: %w", err)
	}
	return mux, nil
}

// parseProtocol normalizes protocol to one of connect's protocol names.
func parseProtocol(protocol string) (string, error) {
	switch protocol {
	case "", "connect":
		return connect.ProtocolNameConnect, nil
	case "grpc":
		return connect.ProtocolNameGRPC, nil
	case "grpcweb", "grpc-web":
		return connect.ProtocolNameGRPCWeb, nil
	default:
		return "", fmt.Errorf("conny: invalid protocol %q (must be connect, grpc, or grpcweb)", protocol)
	}
}

func loadDescriptorSet(path string) (*descriptorpb.FileDescriptorSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading descriptor file: %w", err)
	}
	fds := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, fds); err != nil {
		return nil, fmt.Errorf("unmarshalling descriptor set: %w", err)
	}
	return fds, nil
}
