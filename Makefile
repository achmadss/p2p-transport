VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# CGO stays off everywhere. It is what makes one machine build every target.
export CGO_ENABLED = 0

.PHONY: build test vet cross clean

build:
	go build -ldflags "$(LDFLAGS)" -o dist/ratatoskr ./cmd/ratatoskr
	go build -ldflags "$(LDFLAGS)" -o dist/heimdall  ./cmd/heimdall
	go build -ldflags "$(LDFLAGS)" -o dist/bifrost   ./cmd/bifrost

test:
	go test ./...

vet:
	go vet ./...

cross:
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/ratatoskr-darwin-arm64  ./cmd/ratatoskr
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/ratatoskr-darwin-amd64  ./cmd/ratatoskr
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/ratatoskr-linux-amd64   ./cmd/ratatoskr
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/ratatoskr-linux-arm64   ./cmd/ratatoskr
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/ratatoskr-windows-amd64.exe ./cmd/ratatoskr
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/heimdall-linux-amd64    ./cmd/heimdall
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/bifrost-linux-amd64     ./cmd/bifrost

clean:
	rm -rf dist
