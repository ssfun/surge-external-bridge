GO ?= go
VERSION ?= 0.2.0-dev
CORE_VERSION ?= $(shell $(GO) list -m -f '{{.Version}}' github.com/metacubex/mihomo)
BUILD_TAGS ?=
DIST_DIR ?= dist
BINARY ?= SurgeEB
LDFLAGS = -s -w -X github.com/ssfun/surge-external-bridge/internal/gateway.Version=$(VERSION) -X github.com/ssfun/surge-external-bridge/internal/gateway.BuildVersionMarker=surgeeb-version:$(VERSION)
SURGE_CLI ?= /Applications/Surge.app/Contents/Applications/surge-cli

.PHONY: build frontend frontend-build frontend-test frontend-verify-generated test test-race vet check dist release release-metadata surge-check clean

build:
	CGO_ENABLED=0 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/surgeeb

frontend: frontend-test frontend-build

frontend-test:
	npm ci --prefix frontend
	npm test --prefix frontend

frontend-build:
	npm run build --prefix frontend

frontend-verify-generated:
	bash scripts/validation/frontend-generated.sh

test:
	$(GO) test -tags '$(BUILD_TAGS)' ./...

test-race:
	$(GO) test -race -tags '$(BUILD_TAGS)' ./...

vet:
	$(GO) vet -tags '$(BUILD_TAGS)' ./...

check: frontend-test frontend-verify-generated test vet
	node --check internal/webassets/static/app.js

dist:
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
