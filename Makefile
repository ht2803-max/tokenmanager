VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/ht2803-max/tokenmanager/internal/cli.Version=$(VERSION)

.PHONY: build install test vet fmt cross clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/tokenmanager ./cmd/tokenmanager

install:
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/tokenmanager

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Pure-Go SQLite means no cgo: every target cross-compiles from one machine.
cross:
	GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tokenmanager-darwin-arm64 ./cmd/tokenmanager
	GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tokenmanager-darwin-amd64 ./cmd/tokenmanager
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tokenmanager-linux-amd64 ./cmd/tokenmanager
	GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tokenmanager-linux-arm64 ./cmd/tokenmanager
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tokenmanager-windows-amd64.exe ./cmd/tokenmanager

clean:
	rm -rf bin dist
