package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var Version = "dev"

func main() {
	version := flag.Bool("v", false, "print version")
	port := flag.String("p", envOrDefault("PORT", "8888"), "listen port")
	descriptor := flag.String("d", os.Getenv("DESCRIPTOR"), "path to proto descriptor file")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Conny: A tiny ConnectRPC gateway\n\nUsage: conny -d <descriptor.pb> [flags] <url>\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *version {
		fmt.Println(Version)
		os.Exit(0)
	}

	rawURL := flag.Arg(0)
	if rawURL == "" {
		rawURL = os.Getenv("URL")
	}
	if rawURL == "" || *descriptor == "" {
		flag.Usage()
		os.Exit(1)
	}
	targetURL, err := url.Parse(rawURL)
	if err != nil {
		log.Fatalf("invalid URL: %v", err)
	}

	fds, err := loadDescriptorSet(*descriptor)
	if err != nil {
		log.Fatalf("failed to load descriptor set: %v", err)
	}
	slog.Info("loaded descriptor set", "files", len(fds.GetFile()))

	services, err := buildServices(fds, targetURL)
	if err != nil {
		log.Fatalf("failed to build services: %v", err)
	}
	slog.Info("registered services", "count", len(services))

	transcoder, err := vanguard.NewTranscoder(services,
		vanguard.WithCodec(func(res vanguard.TypeResolver) vanguard.Codec {
			codec := vanguard.NewJSONCodec(res)
			codec.MarshalOptions.UseProtoNames = true
			return codec
		}),
	)
	if err != nil {
		log.Fatalf("failed to create transcoder: %v", err)
	}

	addr := fmt.Sprintf(":%s", *port)
	slog.Info("starting gateway", "addr", addr, "target", rawURL)

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:      addr,
		Handler:   transcoder,
		Protocols: protocols,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

func buildServices(fds *descriptorpb.FileDescriptorSet, targetURL *url.URL) ([]*vanguard.Service, error) {
	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, fmt.Errorf("creating file registry: %w", err)
	}

	types := dynamicpb.NewTypes(files)
	proxy := newReverseProxy(targetURL)

	var services []*vanguard.Service
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		sds := fd.Services()
		for i := range sds.Len() {
			sd := sds.Get(i)
			svc := vanguard.NewServiceWithSchema(
				sd,
				proxy,
				vanguard.WithTargetProtocols(vanguard.ProtocolConnect),
				vanguard.WithTargetCodecs("proto", "json"),
				vanguard.WithTypeResolver(types),
			)
			services = append(services, svc)
			slog.Info("registered service", "name", sd.FullName())
		}
		return true
	})

	if len(services) == 0 {
		return nil, fmt.Errorf("no services found in descriptor set")
	}

	return services, nil
}

func newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
	}
	return proxy
}
