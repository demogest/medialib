# medialib builds. `make` builds the server; `make desktop` the desktop app (needs a C compiler, see CONTRIBUTING.md).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/demogest/medialib/internal/version.Version=$(VERSION)
EXE     := $(if $(filter windows,$(shell go env GOOS)),.exe,)

.PHONY: all server desktop cross winres test e2e vet lint docker clean

all: server

# One static binary with the UI inside: no runtime, no build step for the frontend.
server:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/medialib$(EXE) ./cmd/medialib

desktop:
	scripts/build-desktop.sh dist/medialib-desktop

# Server binaries for every platform (the desktop app has to be built on its own platform).
cross:
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  echo "dist/medialib-$$os-$$arch$$ext"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/medialib-$$os-$$arch$$ext ./cmd/medialib || exit 1; \
	done

# Stamps VERSION (v3.2.0, 3.2.0, or a git describe of one) into the Windows version resource, so a release's .exe
# files show its version under Properties > Details. Release builds run it; other versions keep winres/versioninfo.json's.
winres:
	@set -e; v=$$(echo "$(VERSION)" | sed -nE 's/^v?([0-9]+)\.([0-9]+)\.([0-9]+).*/\1 \2 \3/p'); \
	if [ -z "$$v" ]; then echo "winres: $(VERSION) is not a version number; keeping winres/versioninfo.json's"; exit 0; fi; \
	set -- $$v; cd cmd/medialib; \
	for arch in amd64 arm64; do \
	  arm=; [ $$arch = arm64 ] && arm=-arm; \
	  go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo -64 $$arm \
	    -ver-major=$$1 -ver-minor=$$2 -ver-patch=$$3 -product-ver-major=$$1 -product-ver-minor=$$2 -product-ver-patch=$$3 \
	    -file-version=$$1.$$2.$$3 -product-version=$$1.$$2.$$3 -o resource_windows_$$arch.syso winres/versioninfo.json; \
	done; echo "winres: $$1.$$2.$$3"

vet:
	go vet ./...

# What CI checks besides the tests: Go formatting, that every script of the UI (no build step) and the shell scripts
# at least parse, that the translations match the code (scripts/i18n.mjs), and that VERSION (the last release, see
# CONTRIBUTING.md) is a version. The UI checks need Node 22 or later (module syntax is detected by itself).
lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	@find web -name '*.js' -print0 | xargs -0 -n1 node --check
	@for f in scripts/*.sh; do sh -n "$$f"; done
	@node scripts/i18n.mjs check
	@grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' VERSION || { echo "VERSION must be a version like 3.3.0"; exit 1; }
	@echo lint ok

test: vet
	go test -race ./...

# The whole API against a mock S3 (moto); needs `pip install -r requirements-dev.txt` and ffmpeg.
e2e: server
	MEDIALIB_BIN=$(CURDIR)/dist/medialib$(EXE) python3 -m unittest discover -s tests/e2e -t .

docker:
	docker build -t medialib --build-arg VERSION=$(VERSION) .

clean:
	rm -rf dist
