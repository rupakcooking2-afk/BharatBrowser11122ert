BINARY := browseros
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif
PREFIX ?= $(GOBIN)
VERSION ?= dev
MODULE := github.com/browseros-ai/BrowserOS/packages/browseros/build_go
LDFLAGS := -ldflags "-X $(MODULE)/cmd.Version=$(VERSION)"

DIST_TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: build install uninstall clean test fmt vet dist

build:
	go build $(LDFLAGS) -o $(BINARY) .

install: build
	mkdir -p $(PREFIX)
	cp $(BINARY) $(PREFIX)/$(BINARY)
ifneq ($(shell uname -s),Darwin)
	@echo "Skipping codesign on non-macOS host"
else
	codesign --force --sign - $(PREFIX)/$(BINARY)
endif
	@echo "Installed $(BINARY) to $(PREFIX)/$(BINARY)"

uninstall:
	rm -f $(PREFIX)/$(BINARY)
	@echo "Removed $(PREFIX)/$(BINARY)"

test:
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...

dist:
	@mkdir -p dist
	@for target in $(DIST_TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		out=dist/$(BINARY)-$$os-$$arch; \
		if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
		echo "Building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $$out . || exit 1; \
	done

clean:
	rm -f $(BINARY)
	rm -rf dist
