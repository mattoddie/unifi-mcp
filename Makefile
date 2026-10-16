VERSION ?= 0.0.0-dev
LDFLAGS := -s -w -X github.com/mattoddie/unifi-mcp/internal/tools.Version=$(VERSION)
export CGO_ENABLED := 0

.PHONY: build test lint docs docs-check image clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/unifi-mcp ./cmd/unifi-mcp

test:
	go test ./...

lint:
	gofmt -l . | (! grep .) || (echo "Run gofmt -w ." && exit 1)
	go vet ./...

docs:
	go run ./cmd/gen-tool-docs

docs-check:
	go run ./cmd/gen-tool-docs -check

image:
	docker build --build-arg VERSION=$(VERSION) -t unifi-mcp:$(VERSION) .

clean:
	rm -rf bin
