package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"

	"github.com/connysh/conny"
)

var Version = "dev"

func main() {
	const (
		usageVersion    = "print version"
		usagePort       = "listen port"
		usageDescriptor = "path to proto descriptor file"
		usageProtocol   = "upstream protocol (connect, grpc, grpcweb)"
		usageReflection = "enable server reflection"
		usagePayment    = `upgrade 401 responses with a "Payment" WWW-Authenticate challenge to HTTP 402 (REST clients only)`
		usageStatic     = "directory of static files to serve alongside the RPC routes (e.g. a pre-generated openapi.json)"
	)

	var version bool
	flag.BoolVar(&version, "v", false, usageVersion)
	flag.BoolVar(&version, "version", false, usageVersion)

	var port string
	defaultPort := envOrDefault("PORT", "8888")
	flag.StringVar(&port, "p", defaultPort, usagePort)
	flag.StringVar(&port, "port", defaultPort, usagePort)

	var descriptor string
	defaultDescriptor := os.Getenv("DESCRIPTOR")
	flag.StringVar(&descriptor, "d", defaultDescriptor, usageDescriptor)
	flag.StringVar(&descriptor, "descriptor", defaultDescriptor, usageDescriptor)

	var protocol string
	defaultProtocol := envOrDefault("PROTOCOL", "connect")
	flag.StringVar(&protocol, "protocol", defaultProtocol, usageProtocol)

	var enableReflection bool
	defaultReflection := envOrDefaultBool("REFLECTION", false)
	flag.BoolVar(&enableReflection, "reflection", defaultReflection, usageReflection)

	var enablePayment bool
	defaultPayment := envOrDefaultBool("PAYMENT", false)
	flag.BoolVar(&enablePayment, "payment", defaultPayment, usagePayment)

	var staticDir string
	defaultStatic := os.Getenv("STATIC")
	flag.StringVar(&staticDir, "static", defaultStatic, usageStatic)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Conny: A tiny ConnectRPC gateway\n\nUsage: conny -d <descriptor.pb> [flags] <url>\n\nFlags:\n")
		fmt.Fprintf(os.Stderr, "  -d, --descriptor string\n        %s\n", usageDescriptor)
		fmt.Fprintf(os.Stderr, "  -p, --port string\n        %s (default %q)\n", usagePort, defaultPort)
		fmt.Fprintf(os.Stderr, "      --protocol string\n        %s (default %q)\n", usageProtocol, defaultProtocol)
		fmt.Fprintf(os.Stderr, "      --reflection\n        %s (default %t)\n", usageReflection, defaultReflection)
		fmt.Fprintf(os.Stderr, "      --payment\n        %s (default %t)\n", usagePayment, defaultPayment)
		fmt.Fprintf(os.Stderr, "      --static string\n        %s\n", usageStatic)
		fmt.Fprintf(os.Stderr, "  -v, --version\n        %s\n", usageVersion)
	}
	flag.Parse()

	if version {
		fmt.Println(Version)
		os.Exit(0)
	}

	rawURL := flag.Arg(0)
	if rawURL == "" {
		rawURL = os.Getenv("URL")
	}
	if rawURL == "" || descriptor == "" {
		flag.Usage()
		os.Exit(1)
	}

	cfg := conny.Config{
		DescriptorPath: descriptor,
		Target:         rawURL,
		Protocol:       protocol,
		Reflection:     enableReflection,
		Payment:        enablePayment,
		StaticDir:      staticDir,
	}

	addr := fmt.Sprintf(":%s", port)
	slog.Info("starting gateway", "addr", addr, "target", rawURL, "protocol", protocol)

	if err := conny.ListenAndServe(addr, cfg); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err == nil {
			return parsed
		}
	}
	return fallback
}
