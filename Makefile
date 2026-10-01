# medialib builds. `make` builds the server; `make desktop` the desktop app (needs a C compiler, see README).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/demogest/medialib/internal/version.Version=$(VERSION)
EXE     := $(if $(filter windows,$(shell go env GOOS)),.exe,)

.PHONY: all server desktop cross test e2e vet docker clean

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

vet:
	go vet ./...

test: vet
	go test -race ./...

# The whole API against a mock S3 (moto); needs `pip install -r requirements-dev.txt` and ffmpeg.
e2e: server
	MEDIALIB_BIN=$(CURDIR)/dist/medialib$(EXE) python3 -m unittest discover -s tests/e2e -t .

docker:
	docker build -t medialib --build-arg VERSION=$(VERSION) .

clean:
	rm -rf dist
