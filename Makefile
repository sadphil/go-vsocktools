# Build the vsock tools for Windows and Linux.
#
#   make                  build every program for every OS and architecture
#   make windows          build for windows/amd64 and windows/arm64
#   make linux            build for linux/amd64 and linux/arm64
#   make windows-amd64    build for one OS/architecture (also windows-arm64,
#                         linux-amd64, linux-arm64)
#   make vet test         static checks and tests (vsock integration tests
#                         need the vsock_loopback kernel module)
#   make dist             one archive per OS/architecture in $(BIN)/
#   make clean

GO       ?= go
BIN      ?= bin
PROGRAMS := vsock-tcpproxy vsock-tcplisten vsock-inetd vsock-netcat
OSES     := windows linux
ARCHES   := amd64 arm64
TARGETS  := $(foreach o,$(OSES),$(foreach a,$(ARCHES),$(o)-$(a)))
MODULE   := github.com/sadphil/go-vsocktools
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)
GOBUILD  := CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)'
SOURCES  := go.mod go.sum $(shell find cmd internal -name '*.go')

exe = $(if $(filter windows,$(1)),.exe)

.PHONY: all $(OSES) $(TARGETS) vet test fmt dist clean

all: $(OSES)

$(foreach o,$(OSES),$(eval $(o): $(addprefix $(o)-,$(ARCHES))))

# <os>-<arch>: bin/<os>-<arch>/<program>[.exe] for every program.
define target_rules
$(1)-$(2): $(foreach p,$(PROGRAMS),$(BIN)/$(1)-$(2)/$(p)$(call exe,$(1)))

$(BIN)/$(1)-$(2)/%$(call exe,$(1)): $(SOURCES)
	GOOS=$(1) GOARCH=$(2) $(GOBUILD) -o $$@ ./cmd/$$*
endef
$(foreach o,$(OSES),$(foreach a,$(ARCHES),$(eval $(call target_rules,$(o),$(a)))))

vet:
	$(foreach t,$(TARGETS),CGO_ENABLED=0 GOOS=$(firstword $(subst -, ,$(t))) GOARCH=$(lastword $(subst -, ,$(t))) $(GO) vet ./... &&) true

test:
	$(GO) test ./...

fmt:
	gofmt -l -w cmd internal

dist: all
	$(foreach a,$(ARCHES),cd $(BIN)/windows-$(a) && zip -q ../go-vsocktools-$(VERSION)-windows-$(a).zip *.exe && cd $(CURDIR) &&) true
	$(foreach a,$(ARCHES),tar -C $(BIN)/linux-$(a) -czf $(BIN)/go-vsocktools-$(VERSION)-linux-$(a).tar.gz $(PROGRAMS) &&) true

clean:
	rm -rf $(BIN)
