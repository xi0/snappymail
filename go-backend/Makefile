# Go backend of SnappyMail.
#
# `make` builds the binary, `make test` runs the verification shape required by
# SPEC.md (build + vet + test). The diff harness (`make diff-record` /
# `make diff-test`) is added in step 33.

GO     ?= go
BINARY ?= go-backend
PKG    := ./...

.PHONY: all build build-release test vet test-unit fmt clean

all: build

build:
	$(GO) build -trimpath -o $(BINARY) .

# Release binaries are static and portable: no cgo (see SPEC.md).
build-release:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(BINARY) .

# One step = one verification shape: build, vet and test everything.
test: vet
	$(GO) test $(PKG)

vet:
	$(GO) build $(PKG)
	$(GO) vet $(PKG)

test-unit:
	$(GO) test $(PKG)

fmt:
	$(GO) fmt $(PKG)

clean:
	$(GO) clean
	rm -f $(BINARY)
