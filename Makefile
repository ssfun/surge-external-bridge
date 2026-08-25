GO ?= go
VERSION ?= 0.2.0-dev
CORE_VERSION ?= $(shell $(GO) list -m -f '{{.Version}}' github.com/metacubex/mihomo)
BUILD_TAGS ?=
DIST_DIR ?= dist
LDFLAGS = -s -w -X github.com/ssfun/vless2surge/internal/gateway.Version=$(VERSION) -X github.com/ssfun/vless2surge/internal/gateway.BuildVersionMarker=vless2surge-version:$(VERSION)
SURGE_CLI ?= /Applications/Surge.app/Contents/Applications/surge-cli

.PHONY: build test test-race vet check dist release release-metadata surge-check clean

build:
	CGO_ENABLED=0 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o vless2surge ./cmd/vless2surge

test:
	$(GO) test -tags '$(BUILD_TAGS)' ./...

test-race:
	$(GO) test -race -tags '$(BUILD_TAGS)' ./...

vet:
	$(GO) vet -tags '$(BUILD_TAGS)' ./...

check: test vet
	node --check internal/webassets/static/app.js

dist:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/vless2surge-darwin-arm64 ./cmd/vless2surge
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/vless2surge-darwin-amd64 ./cmd/vless2surge
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/vless2surge-linux-arm64 ./cmd/vless2surge
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/vless2surge-linux-amd64 ./cmd/vless2surge

release: dist release-metadata

release-metadata:
	$(GO) run ./cmd/releasegen --dist $(DIST_DIR) --core-version $(CORE_VERSION) --build-tags '$(BUILD_TAGS)' --version '$(VERSION)'

surge-check:
	test -x '$(SURGE_CLI)'
	'$(SURGE_CLI)' --check testdata/surge-policy-matrix.conf

clean:
	rm -rf dist vless2surge
