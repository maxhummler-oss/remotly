VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GO      ?= go
DIST    := dist

AGENT_TARGETS  := windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64
SERVER_TARGETS := linux/amd64 linux/arm64

.PHONY: all server agents package test vet clean

all: server agents

# Server binaries: dist/remotly-server-linux-<arch>
server:
	@for t in $(SERVER_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "server  $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(DIST)/remotly-server-$$os-$$arch ./cmd/remotly-server || exit 1; \
	done

# Agent binaries: dist/agents/remotly-agent-<os>-<arch>[.exe]
# All targets are pure Go (no cgo), so everything cross-compiles from Linux.
agents:
	@mkdir -p $(DIST)/agents
	@for t in $(AGENT_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; flags="$(LDFLAGS)"; \
		if [ $$os = windows ]; then ext=.exe; flags="$$flags -H windowsgui"; fi; \
		echo "agent   $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$$flags" \
			-o $(DIST)/agents/remotly-agent-$$os-$$arch$$ext ./cmd/remotly-agent || exit 1; \
	done

# Release archives: dist/remotly-<version>-linux-<arch>.tar.gz containing the
# server, all agents and the installer.
package: all
	@for t in $(SERVER_TARGETS); do \
		arch=$${t#*/}; d=$(DIST)/pkg-$$arch/remotly; \
		rm -rf $(DIST)/pkg-$$arch; mkdir -p $$d; \
		cp $(DIST)/remotly-server-linux-$$arch $$d/remotly-server; \
		cp -r $(DIST)/agents $$d/agents; \
		cp scripts/install-server.sh $$d/install.sh; \
		cp README.md LICENSE $$d/ 2>/dev/null || true; \
		tar -C $(DIST)/pkg-$$arch -czf $(DIST)/remotly-linux-$$arch.tar.gz remotly; \
		rm -rf $(DIST)/pkg-$$arch; \
		echo "package $(DIST)/remotly-linux-$$arch.tar.gz"; \
	done

test:
	$(GO) test -race ./...

vet:
	@for t in $(AGENT_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) vet ./... || exit 1; \
	done

clean:
	rm -rf $(DIST)
