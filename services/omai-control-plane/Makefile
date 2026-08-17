SHELL := /bin/sh
BUF_VERSION := v1.72.0
VERSION ?= dev
NPM_CACHE ?= $(CURDIR)/.cache/npm

.PHONY: bootstrap generate lint test verify build run run-voice run-executor run-adk clean adk-lint adk-test adk-build sdk-install sdk-verify sdk-build

bootstrap:
	go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

generate:
	buf lint
	buf generate

lint:
	gofmt -w $$(find . -name '*.go' -not -path './gen/*')
	go vet ./...

test:
	go test -race -count=1 ./...

verify: generate lint test adk-lint adk-test sdk-verify
	go build -buildvcs=false ./...
	cd integration/adk && go build -buildvcs=false ./...

build: generate
	mkdir -p bin
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w -X main.version=$(VERSION)' -o bin/omai ./cmd/omai-server
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w -X main.version=$(VERSION)' -o bin/omai-workspace-executor ./cmd/omai-workspace-executor
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o bin/omai-voice-gateway ./cmd/omai-voice-gateway
	cd integration/adk && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w -X main.version=$(VERSION)' -o ../../bin/omai-adk-runtime ./cmd/omai-adk-runtime

run: generate
	go run ./cmd/omai-server

run-voice: generate
	go run ./cmd/omai-voice-gateway

run-executor: generate
	go run ./cmd/omai-workspace-executor

run-adk: generate
	cd integration/adk && go run ./cmd/omai-adk-runtime

adk-test: generate
	cd integration/adk && go test -race -count=1 ./...

adk-lint: generate
	cd integration/adk && go vet ./...

adk-build: generate
	mkdir -p bin
	cd integration/adk && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w -X main.version=$(VERSION)' -o ../../bin/omai-adk-runtime ./cmd/omai-adk-runtime

sdk-install:
	cd sdk/typescript && npm ci --cache "$(NPM_CACHE)" --no-audit --no-fund

sdk-verify: sdk-install
	cd sdk/typescript && NPM_CONFIG_CACHE="$(NPM_CACHE)" npm run verify

sdk-build: sdk-install generate
	cd sdk/typescript && npm run build

clean:
	rm -rf bin gen/go/uab coverage.out sdk/typescript/.cache sdk/typescript/dist sdk/typescript/node_modules sdk/typescript/src/gen
