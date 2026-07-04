# Conny

A tiny [ConnectRPC](https://connectrpc.com) gateway. Translates REST, gRPC, and gRPC-Web requests into the Connect, gRPC, or gRPC-Web protocols using a protobuf descriptor. 

## Install

**Homebrew:**

```sh
brew install connysh/tap/conny
```

**Binary** (from [GitHub Releases](https://github.com/connysh/conny/releases)):

```sh
# macOS / Linux
curl -fsSL https://github.com/connysh/conny/releases/latest/download/conny_$(uname -s)_$(uname -m).tar.gz | tar xz
```

**Docker:**

```sh
docker pull ghcr.io/connysh/conny:latest
```

**Go:**

```sh
go install github.com/connysh/conny/cmd/conny@latest
```

## Usage

```sh
conny -d descriptor.pb http://localhost:8080
```

Use `h2c://` for upstream servers that require HTTP/2 over plaintext (e.g. gRPC with streaming):

```sh
conny -d descriptor.pb h2c://localhost:8080
```

### Flags

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `-d, --descriptor` | `DESCRIPTOR` | | Path to proto descriptor file |
| `-p, --port`       | `PORT`       | `8888` | Listen port |
| `--protocol`       | `PROTOCOL`   | `connect` | Upstream protocol (`connect`, `grpc`, `grpcweb`) |
| `--reflection`     | `REFLECTION` | `false` | Enable server reflection |
| `--payment`        | `PAYMENT`    | `false` | Upgrade 401 responses with a `Payment` `WWW-Authenticate` challenge to HTTP 402 (REST clients only) |
| `-v, --version`    | | | Print version |

The backend URL can also be set via the `URL` environment variable.

### Health check

`GET /health` returns `200 OK` with body `ok`.

### Generate a descriptor

```sh
buf build -o descriptor.pb
```

### Docker

```sh
docker run -v ./descriptor.pb:/descriptor.pb ghcr.io/connysh/conny \
  -d /descriptor.pb http://backend:8080
```

### Kubernetes

```yaml
containers:
  - name: conny
    image: ghcr.io/connysh/conny:latest
    args: ["-d", "/etc/conny/descriptor.pb", "http://backend:8080"]
    volumeMounts:
      - name: descriptor
        mountPath: /etc/conny
volumes:
  - name: descriptor
    configMap:
      name: conny-descriptor
```

## Use as a library

conny can also be embedded in a Go program. Build a `conny.Config` and either
serve it directly or mount its `http.Handler` in your own server:

```go
package main

import (
	"log"
	"net/http"

	"github.com/connysh/conny"
)

func main() {
	cfg := conny.Config{
		DescriptorPath: "descriptor.pb",     // or Descriptor: fds
		Target:         "h2c://localhost:8080",
		Protocol:       "connect",           // connect | grpc | grpcweb
		Reflection:     true,
		Payment:        true,
	}

	// Serve directly (HTTP/1 + h2c, blocks):
	log.Fatal(conny.ListenAndServe(":8888", cfg))

	// ...or mount the handler in your own mux / middleware stack:
	h, err := conny.NewHandler(cfg)
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/api/", http.StripPrefix("/api", h))
}
```

Every CLI flag has a `Config` field. Provide the descriptor either as a path
(`DescriptorPath`) or an in-memory `*descriptorpb.FileDescriptorSet`
(`Descriptor`). Set `Logger` to route structured logs; it defaults to
`slog.Default()`.

## License

[Apache 2.0](LICENSE)
