GO ?= go
VERSION ?= 0.2.0-dev
CORE_VERSION ?= $(shell $(GO) list -m -f '{{.Version}}' github.com/metacubex/mihomo)
BUILD_TAGS ?=
DIST_DIR ?= dist
BINARY ?= SurgeEB
LDFLAGS = -s -w -X github.com/ssfun/surge-external-bridge/internal/gateway.Version=$(VERSION) -X github.com/ssfun/surge-external-bridge/internal/gateway.BuildVersionMarker=surgeeb-version:$(VERSION)
SURGE_CLI ?= /Applications/Surge.app/Contents/Applications/surge-cli

.PHONY: build frontend frontend-install frontend-build frontend-test go-test go-test-race go-vet test test-race vet check dist dist-platform release release-metadata surge-check clean

build: frontend-build
	CGO_ENABLED=0 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/surgeeb

frontend: frontend-test frontend-build

frontend-install:
	npm ci --prefix frontend

frontend-test: frontend-install
	npm test --prefix frontend

frontend-build: frontend-install
	npm run build --prefix frontend

go-test:
	$(GO) test -tags '$(BUILD_TAGS)' ./...

go-test-race:
	$(GO) test -race -tags '$(BUILD_TAGS)' ./...

go-vet:
	$(GO) vet -tags '$(BUILD_TAGS)' ./...

test: frontend-build go-test

test-race: frontend-build go-test-race

vet: frontend-build go-vet

check: frontend-test test vet
	node --check internal/webassets/static/app.js

dist: frontend-build
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/SurgeEB-darwin-arm64 ./cmd/surgeeb
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/SurgeEB-darwin-amd64 ./cmd/surgeeb
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/SurgeEB-linux-arm64 ./cmd/surgeeb
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/SurgeEB-linux-amd64 ./cmd/surgeeb

dist-platform:
	case '$(TARGET_GOOS)/$(TARGET_GOARCH)' in darwin/arm64|darwin/amd64|linux/arm64|linux/amd64) ;; *) echo 'unsupported target: $(TARGET_GOOS)/$(TARGET_GOARCH)' >&2; exit 1 ;; esac
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS='$(TARGET_GOOS)' GOARCH='$(TARGET_GOARCH)' $(GO) build -tags '$(BUILD_TAGS)' -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/SurgeEB-$(TARGET_GOOS)-$(TARGET_GOARCH) ./cmd/surgeeb

release: dist release-metadata

release-metadata:
	$(GO) run ./cmd/releasegen --dist $(DIST_DIR) --core-version $(CORE_VERSION) --build-tags '$(BUILD_TAGS)' --version '$(VERSION)'

surge-check:
	test -x '$(SURGE_CLI)'
	'$(SURGE_CLI)' --check testdata/surge-policy-matrix.conf

clean:
	rm -rf dist SurgeEB
