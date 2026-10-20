package conny

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"connectrpc.com/grpcreflect/v2"
	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// newServer registers a method for every RPC in files that relays the call to
// upstream, plus gRPC server reflection when enabled.
func newServer(files *protoregistry.Files, types *dynamicpb.Types, upstream *connect.Client, enableReflection bool, logger *slog.Logger) (*connect.Server, int, error) {
	server := connect.NewServer()
	var serviceNames []string

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		sds := fd.Services()
		for i := range sds.Len() {
			sd := sds.Get(i)
			serviceNames = append(serviceNames, string(sd.FullName()))
			server.Register(vanguard.ForwardService(upstream, sd)...)
			logger.Info("registered service", "name", sd.FullName())
		}
		return true
	})

	if len(serviceNames) == 0 {
		return nil, 0, fmt.Errorf("no services found in descriptor set")
	}

	if enableReflection {
		// The descriptor set can't resolve the reflection services themselves,
		// so list only the upstream's.
		grpcreflect.Register(server,
			grpcreflect.WithNamer(grpcreflect.NamerFunc(func() []string { return serviceNames })),
			grpcreflect.WithDescriptorResolver(files),
			grpcreflect.WithExtensionResolver(&extensionResolver{types}),
		)
		logger.Info("registered reflection service", "versions", "v1, v1alpha")
	}

	return server, len(serviceNames), nil
}

func newUpstreamClient(target *url.URL, protocol string, types *dynamicpb.Types, enableH2C bool, logger *slog.Logger) *connect.Client {
	httpClient := &http.Client{}
	if enableH2C {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Protocols = new(http.Protocols)
		transport.Protocols.SetUnencryptedHTTP2(true)
		httpClient.Transport = transport
	}

	options := []connecthttp.Option{
		connecthttp.WithCodecs(
			connectproto.NewBinaryCodec(connectproto.WithTypeResolver(types)),
			connectproto.NewJSONCodec(connectproto.WithTypeResolver(types)),
		),
	}
	switch protocol {
	case connect.ProtocolNameGRPC:
		options = append(options, connecthttp.WithGRPC())
	case connect.ProtocolNameGRPCWeb:
		options = append(options, connecthttp.WithGRPCWeb())
	}

	transport := connecthttp.NewTransport(httpClient, target.String(), options...)
	return connect.NewClient(transport, logUpstreamErrors(logger))
}

// logUpstreamErrors logs failures to reach the upstream. Errors the upstream
// itself returned are the client's business and pass through unlogged.
func logUpstreamErrors(logger *slog.Logger) connect.ClientInterceptor {
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			stream, err := next(ctx, spec)
			if err != nil {
				logUpstreamError(logger, spec, err)
				return nil, err
			}
			return &loggingClientStream{ClientStream: stream, spec: spec, logger: logger}, nil
		}
	}
}

type loggingClientStream struct {
	connect.ClientStream
	spec   connect.Spec
	logger *slog.Logger
}

func (s *loggingClientStream) Receive(msg any) error {
	err := s.ClientStream.Receive(msg)
	if err != nil && !errors.Is(err, io.EOF) {
		logUpstreamError(s.logger, s.spec, err)
	}
	return err
}

func logUpstreamError(logger *slog.Logger, spec connect.Spec, err error) {
	if connectErr, ok := errors.AsType[*connect.Error](err); ok && connectErr.IsRemote() {
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	logger.Error("upstream error", "procedure", spec.Procedure, "error", err)
}

type extensionResolver struct {
	*dynamicpb.Types
}

func (e *extensionResolver) RangeExtensionsByMessage(message protoreflect.FullName, f func(protoreflect.ExtensionType) bool) {
}
