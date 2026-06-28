#!/bin/bash
APP_NAME := dr

VERSION :=$(shell git describe --match 'v[0-9]*' --dirty='.m' --always)
REVISION :=$(shell git rev-parse HEAD)$(shell if git diff --no-ext-diff --quiet --exit-code; then echo .m; fi)
DATE :=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

OS ?=$(shell uname -s | tr '[:upper:]' '[:lower:]')
ARCH ?=$(shell go env GOARCH | tr '[:upper:]' '[:lower:]')

build:
	@if [ -z "$(ARCH)" ]; then echo "mandatory ARCH field is empty"; exit 1; fi
	@if [ -z "$(OS)" ]; then echo "mandatory OS field is empty"; exit 1; fi
	@echo "Building for OS=$(OS), ARCH=$(ARCH), VERSION=$(VERSION), REVISION=$(REVISION)"
	@GOOS=$(OS) GOARCH=$(ARCH) go build \
	-ldflags "-X main.Version=$(VERSION) -X main.Revision=$(REVISION) -X main.Date=$(DATE) -s -w" \
	-trimpath -a -o $(APP_NAME)

test:
	go test ./... -race

vet:
	go vet ./...

fmt:
	gofmt -w .

.PHONY: build test vet fmt

