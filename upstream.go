package conny

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	_ "buf.build/gen/go/grpc/grpc/protocolbuffers/go/grpc/reflection/v1"
	"connectrpc.com/grpcreflect"
	"connectrpc.com/vanguard"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

func buildServices(files *protoregistry.Files, targetURL *url.URL, protocol vanguard.Protocol, enableReflection, enableH2C bool, logger *slog.Logger) ([]*vanguard.Service, error) {
	types := dynamicpb.NewTypes(files)
	proxy := newReverseProxy(targetURL, enableH2C, logger)

	var services []*vanguard.Service
	var serviceNames []string

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		sds := fd.Services()
		for i := range sds.Len() {
			sd := sds.Get(i)
			serviceNames = append(serviceNames, string(sd.FullName()))
			svc := vanguard.NewServiceWithSchema(
				sd,
				proxy,
				vanguard.WithTargetProtocols(protocol),
				vanguard.WithTargetCodecs("proto", "json"),
				vanguard.WithTypeResolver(types),
				vanguard.WithRESTUnmarshalOptions(vanguard.RESTUnmarshalOptions{DiscardUnknownQueryParams: true}),
			)
			services = append(services, svc)
			logger.Info("registered service", "name", sd.FullName())
		}
		return true
	})

	if len(services) == 0 {
		return nil, fmt.Errorf("no services found in descriptor set")
	}

	if enableReflection {
		reflector := grpcreflect.NewReflector(
			&namer{services: serviceNames},
			grpcreflect.WithDescriptorResolver(files),
			grpcreflect.WithExtensionResolver(&extensionResolver{types}),
		)

		v1Path, v1Handler := grpcreflect.NewHandlerV1(reflector)
		services = append(services, vanguard.NewService(v1Path, v1Handler))
		logger.Info("registered reflection service", "version", "v1", "path", v1Path)
	}

	return services, nil
}

func newReverseProxy(target *url.URL, enableH2C bool, logger *slog.Logger) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Error("upstream error", "method", r.Method, "path", r.URL.Path, "error", err)
		w.WriteHeader(http.StatusBadGateway)
	}
	if enableH2C {
		proxy.Transport = &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr)
			},
		}
	}
	return proxy
}

type extensionResolver struct {
	*dynamicpb.Types
}

func (e *extensionResolver) RangeExtensionsByMessage(message protoreflect.FullName, f func(protoreflect.ExtensionType) bool) {
}

type namer struct {
	services []string
}

func (n *namer) Names() []string {
	return n.services
}
