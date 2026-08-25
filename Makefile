GO ?= go
VERSION ?= 0.2.0-dev
CORE_VERSION ?= $(shell $(GO) list -m -f '{{.Version}}' github.com/metacubex/mihomo)
BUILD_TAGS ?=
DIST_DIR ?= dist
BINARY ?= SurgeEB
LDFLAGS = -s -w -X github.com/ssfun/surge-external-bridge/internal/gateway.Version=$(VERSION) -X github.com/ssfun/surge-external-bridge/internal/gateway.BuildVersionMarker=surgeeb-version:$(VERSION)
SURGE_CLI ?= /Applications/Surge.app/Contents/Applications/surge-cli

.PHONY: build frontend frontend-install frontend-build frontend-test test test-race vet check dist release release-metadata surge-check clean

build: frontend-build
	CGO_ENABLED=0 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/surgeeb

frontend: frontend-test frontend-build

frontend-install:
	npm ci --prefix frontend

frontend-test: frontend-install
	npm test --prefix frontend

frontend-build: frontend-install
	npm run build --prefix frontend

test: frontend-build
	$(GO) test -tags '$(BUILD_TAGS)' ./...

test-race: frontend-build
	$(GO) test -race -tags '$(BUILD_TAGS)' ./...

vet: frontend-build
	$(GO) vet -tags '$(BUILD_TAGS)' ./...

check: frontend-test test vet
	node --check internal/webassets/static/app.js

dist: frontend-build
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/SurgeEB-darwin-arm64 ./cmd/surgeeb
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/SurgeEB-darwin-amd64 ./cmd/surgeeb
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/SurgeEB-linux-arm64 ./cmd/surgeeb
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/SurgeEB-linux-amd64 ./cmd/surgeeb

release: dist release-metadata

release-metadata:
	$(GO) run ./cmd/releasegen --dist $(DIST_DIR) --core-version $(CORE_VERSION) --build-tags '$(BUILD_TAGS)' --version '$(VERSION)'

surge-check:
	test -x '$(SURGE_CLI)'
	'$(SURGE_CLI)' --check testdata/surge-policy-matrix.conf

clean:
	rm -rf dist SurgeEB
