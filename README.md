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
go install github.com/connysh/conny@latest
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
| `-H, --header`     | `HEADER_*`   | | Add upstream header (`"Name: Value"`), repeatable |
| `--protocol`       | `PROTOCOL`   | `connect` | Upstream protocol (`connect`, `grpc`, `grpcweb`) |
| `--reflection`     | `REFLECTION` | `false` | Enable server reflection |
| `-v, --version`    | | | Print version |

The backend URL can also be set via the `URL` environment variable.

### Upstream headers

Add headers to every upstream request via the `-H` flag or `HEADER_<NAME>` environment variables:

```sh
conny -d descriptor.pb -H "X-Gateway-Token: secret" http://localhost:8080
```

`HEADER_*` env vars are useful when the value comes from a secret manager (e.g. Cloud Run + Secret Manager, Kubernetes secrets):

```sh
HEADER_X_GATEWAY_TOKEN=secret conny -d descriptor.pb http://localhost:8080
# adds X-Gateway-Token: secret to every upstream request
```

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

## License

[Apache 2.0](LICENSE)
