default:
    @just --list

build:
    go build -trimpath -ldflags "-s -w" -o conny .

vet:
    go vet ./...

run *args:
    go run . {{args}}
