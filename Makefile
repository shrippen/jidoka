.PHONY: check lint test run build

check: lint test

# gofmt of go.mod's toolchain, as in CI.
GOFMT = $(shell GOTOOLCHAIN=$$(awk '/^toolchain/ {print $$2}' go.mod) go env GOROOT)/bin/gofmt

lint:
	$(GOFMT) -l cmd internal | grep . && exit 1 || true
	go vet ./...
	go vet -tags release ./...
	# Same version as CI (.github/workflows/ci.yml).
	go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...

test:
	go test ./...

run:
	DATA_DIR=./data JOBS_DIR=./demo/jobs JIDOKA_DEV=true MASTER_KEY=dev-only-not-secret go run ./cmd/jidoka serve

build:
	CGO_ENABLED=0 go build -tags release -trimpath -ldflags="-s -w" -o bin/jidoka ./cmd/jidoka
	scripts/release-check.sh bin/jidoka
