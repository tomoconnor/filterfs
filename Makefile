PREFIX     ?= /usr/local
BINDIR     ?= $(PREFIX)/bin
SBINDIR    ?= /sbin
DESTDIR    ?=
GO         ?= go
BIN        := bin/filterfs
GO_IMAGE   ?= golang:1.24-bookworm

.PHONY: all build test vet integration docker-integration install install-mount-helper uninstall clean

all: build

build:
	$(GO) build -o $(BIN) ./cmd/filterfs

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Needs Linux and a usable /dev/fuse; skips itself otherwise.
integration: build
	$(GO) test -count=1 -v -run . ./internal/fs/
	scripts/integration.sh $(BIN)

# Run the integration tests in a privileged Linux container (e.g. on macOS).
docker-integration:
	docker run --rm --device /dev/fuse --cap-add SYS_ADMIN \
		--security-opt apparmor:unconfined \
		-v "$(CURDIR)":/src -w /src \
		-v filterfs-gomod:/go/pkg/mod -e GOFLAGS=-buildvcs=false \
		$(GO_IMAGE) sh -c 'apt-get update -qq && apt-get install -y -qq fuse3 procps >/dev/null && \
			make integration BIN=/tmp/filterfs && go vet ./...'

install: build
	install -d $(DESTDIR)$(BINDIR)
	install -m 0755 $(BIN) $(DESTDIR)$(BINDIR)/filterfs

# Optional: lets "mount -t filterfs -o exclude=/.git SRC DST" and fstab work.
install-mount-helper: install
	install -d $(DESTDIR)$(SBINDIR)
	ln -sf $(BINDIR)/filterfs $(DESTDIR)$(SBINDIR)/mount.filterfs

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/filterfs
	[ ! -L $(DESTDIR)$(SBINDIR)/mount.filterfs ] || rm -f $(DESTDIR)$(SBINDIR)/mount.filterfs

clean:
	rm -rf bin
