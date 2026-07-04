default:
    @just --list

build:
    go build -trimpath -ldflags "-s -w" -o conny ./cmd/conny

vet:
    go vet ./...

test:
    go test ./...

run *args:
    go run ./cmd/conny {{args}}
