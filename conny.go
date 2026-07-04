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

	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
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

	// Reflection enables gRPC server reflection (v1).
	Reflection bool

	// Payment upgrades REST responses to 402 Payment Required when the upstream
	// returns a 401 carrying a "Payment" WWW-Authenticate challenge.
	Payment bool

	// Logger receives structured logs. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

func (c Config) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// NewHandler builds the gateway's http.Handler — /health plus the transcoder —
// for mounting in your own server or middleware stack. To accept gRPC or h2c
// clients, enable unencrypted HTTP/2 on your server (see [ListenAndServe]).
func NewHandler(c Config) (http.Handler, error) {
	logger := c.logger()

	fds, err := c.resolveDescriptor()
	if err != nil {
		return nil, err
	}
	logger.Info("loaded descriptor set", "files", len(fds.GetFile()))

	target, enableH2C, err := c.resolveTarget()
	if err != nil {
		return nil, err
	}

	proto, err := parseProtocol(c.Protocol)
	if err != nil {
		return nil, err
	}

	services, err := buildServices(fds, target, proto, c.Reflection, enableH2C, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("registered services", "count", len(services))

	transcoder, err := vanguard.NewTranscoder(services,
		vanguard.WithCodec(func(res vanguard.TypeResolver) vanguard.Codec {
			codec := vanguard.NewJSONCodec(res)
			codec.MarshalOptions.UseProtoNames = true
			codec.MarshalOptions.EmitUnpopulated = true
			codec.UnmarshalOptions.DiscardUnknown = true
			return codec
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("conny: creating transcoder: %w", err)
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

	var rootHandler http.Handler = transcoder
	if c.Payment {
		rootHandler = withPaymentRequired(transcoder)
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

func parseProtocol(protocol string) (vanguard.Protocol, error) {
	switch protocol {
	case "", "connect":
		return vanguard.ProtocolConnect, nil
	case "grpc":
		return vanguard.ProtocolGRPC, nil
	case "grpcweb", "grpc-web":
		return vanguard.ProtocolGRPCWeb, nil
	default:
		return 0, fmt.Errorf("conny: invalid protocol %q (must be connect, grpc, or grpcweb)", protocol)
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
