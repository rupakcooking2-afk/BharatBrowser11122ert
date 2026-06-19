BINARY := browseros-patch
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif
PREFIX ?= $(GOBIN)
VERSION ?= dev

.PHONY: build install uninstall clean test fmt

build:
	go build -ldflags "-X github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/cmd.Version=$(VERSION)" -o $(BINARY) .

install: build
	mkdir -p $(PREFIX)
	cp $(BINARY) $(PREFIX)/$(BINARY)
ifneq ($(shell uname -s),Darwin)
	@echo "Skipping codesign on non-macOS host"
else
	codesign --force --sign - $(PREFIX)/$(BINARY)
endif
	ln -sf $(PREFIX)/$(BINARY) $(PREFIX)/bpatch
	@echo "Installed $(BINARY) to $(PREFIX)/$(BINARY) (alias: bpatch)"

uninstall:
	rm -f $(PREFIX)/$(BINARY) $(PREFIX)/bpatch
	@echo "Removed $(PREFIX)/$(BINARY) and $(PREFIX)/bpatch"

test:
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

clean:
	rm -f $(BINARY)
