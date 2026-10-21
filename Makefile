VERSION ?= 0.0.0-dev
LDFLAGS := -s -w -X github.com/mattoddie/unifi-mcp/internal/tools.Version=$(VERSION)
export CGO_ENABLED := 0

# Platforms for the MCP Bundles (one .mcpb each), as GOOS/GOARCH.
MCPB_PLATFORMS := darwin/arm64 darwin/amd64 windows/amd64 windows/arm64 linux/amd64 linux/arm64

.PHONY: build test lint docs docs-check image mcpb clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/unifi-mcp ./cmd/unifi-mcp

test:
	go test ./...

lint:
	gofmt -l . | (! grep .) || (echo "Run gofmt -w ." && exit 1)
	go vet ./...

docs:
	go run ./cmd/gen-tool-docs
	go run ./cmd/mcpb manifest

docs-check:
	go run ./cmd/gen-tool-docs -check
	go run ./cmd/mcpb manifest -check

image:
	docker build --build-arg VERSION=$(VERSION) -t unifi-mcp:$(VERSION) .

# MCP Bundles for Claude Desktop: dist/unifi-mcp-$(VERSION)-<os>-<arch>.mcpb.
mcpb:
	rm -rf .build/mcpb dist/*.mcpb
	go build -o .build/mcpb/pack ./cmd/mcpb
	set -e; for p in $(MCPB_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; \
		if [ "$$os" = windows ]; then ext=.exe; fi; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o .build/mcpb/$$os-$$arch/unifi-mcp$$ext ./cmd/unifi-mcp; \
		.build/mcpb/pack pack -version $(VERSION) -goos $$os -binary .build/mcpb/$$os-$$arch/unifi-mcp$$ext \
			-o dist/unifi-mcp-$(VERSION)-$$os-$$arch.mcpb; \
	done

clean:
	rm -rf bin .build dist
