BINARY := anvil
DIST   := dist
PKG    := .
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# darwin needs CGO (go-gui's Metal backend is Objective-C), so build-darwin-* only works on a Mac
export CGO_ENABLED := $(if $(filter Darwin,$(shell uname -s)),1,0)
GOFLAGS := -trimpath
LDFLAGS := -s -w -X main.version=$(VERSION)

PLATFORMS := linux-amd64 linux-arm64 windows-amd64 windows-arm64 darwin-amd64 darwin-arm64
TARGETS   := $(PLATFORMS:%=build-%)

.PHONY: all build run clean checksums release $(TARGETS)

all: $(TARGETS)

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) $(PKG)

run:
	go run $(PKG)

# build-<os>-<arch>, e.g. build-linux-amd64 → dist/anvil-linux-amd64
$(TARGETS): build-%:
	CGO_ENABLED=$(if $(findstring darwin,$*),1,0) GOOS=$(word 1,$(subst -, ,$*)) GOARCH=$(word 2,$(subst -, ,$*)) \
		go build $(GOFLAGS) -ldflags "$(LDFLAGS)$(if $(findstring windows,$*), -H=windowsgui)" \
		-o $(DIST)/$(BINARY)-$*$(if $(findstring windows,$*),.exe) $(PKG)

checksums:
	cd $(DIST) && sha256sum $(BINARY)-* > SHA256SUMS

# release: builds every platform from scratch and generates SHA256SUMS
release: clean
	$(MAKE) all
	$(MAKE) checksums

clean:
	rm -rf $(DIST)
