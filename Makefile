# Build the vsock tools for Microsoft Windows guests.
#
#   make                  build every program for every architecture
#   make windows-amd64    build only for x64
#   make windows-arm64    build only for ARM64
#   make vet test         static checks and unit tests
#   make dist             zip archives per architecture in $(BIN)/
#   make clean

GO       ?= go
BIN      ?= bin
PROGRAMS := vsock-tcpproxy vsock-tcplisten vsock-inetd vsock-netcat
ARCHES   := amd64 arm64
MODULE   := github.com/sadphil/go-winvsocktools
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)
GOBUILD  := CGO_ENABLED=0 GOOS=windows $(GO) build -trimpath -ldflags '$(LDFLAGS)'
SOURCES  := go.mod go.sum $(shell find cmd internal -name '*.go')

.PHONY: all windows $(addprefix windows-,$(ARCHES)) vet test fmt dist clean

all: windows

windows: $(addprefix windows-,$(ARCHES))

# windows-<arch>: bin/windows-<arch>/<program>.exe for every program.
define arch_rules
windows-$(1): $(foreach p,$(PROGRAMS),$(BIN)/windows-$(1)/$(p).exe)

$(BIN)/windows-$(1)/%.exe: $(SOURCES)
	GOARCH=$(1) $(GOBUILD) -o $$@ ./cmd/$$*
endef
$(foreach a,$(ARCHES),$(eval $(call arch_rules,$(a))))

vet:
	$(foreach a,$(ARCHES),GOOS=windows GOARCH=$(a) $(GO) vet ./... &&) true

test:
	$(GO) test ./...

fmt:
	gofmt -l -w cmd internal

dist: windows
	$(foreach a,$(ARCHES),cd $(BIN)/windows-$(a) && zip -q ../go-winvsocktools-$(VERSION)-windows-$(a).zip *.exe && cd $(CURDIR) &&) true

clean:
	rm -rf $(BIN)
