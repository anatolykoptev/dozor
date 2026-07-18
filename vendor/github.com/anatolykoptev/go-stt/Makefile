.PHONY: build test lint preflight fmt vet golangci-lint clean

# Single-package library — all build/test/lint targets scope to "."
# (never "./..." for build/test, per repo convention).

build:
	GOWORK=off go build .

test:
	GOWORK=off go test -race -count=1 .

# lint runs errcheck (-blank) and staticcheck (SA4006, SA4008).
# Pinned versions (NEVER @latest — gostall pattern, prevents drift).
# Tools are resolved via GOPATH/bin (self-hosted runner PATH may not include it).
ERRCHECK_VERSION := v1.1.0
STATICCHECK_VERSION := 2025.1.7
ERRCHECK := $(shell command -v errcheck 2>/dev/null || echo $$(go env GOPATH)/bin/errcheck)
STATICCHECK := $(shell command -v staticcheck 2>/dev/null || echo $$(go env GOPATH)/bin/staticcheck)

lint:
	@[ -x "$(ERRCHECK)" ] || { echo "install hint: go install github.com/kisielk/errcheck@$(ERRCHECK_VERSION)"; exit 1; }
	@[ -x "$(STATICCHECK)" ] || { echo "install hint: go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)"; exit 1; }
	"$(ERRCHECK)" -blank .
	"$(STATICCHECK)" -checks SA4006,SA4008 .

# golangci-lint is kept for local use only — it is NOT part of preflight
# because it would need a separate install step in CI.
golangci-lint:
	GOWORK=off golangci-lint run .

# fmt fails the build if any file needs gofmt formatting.
fmt:
	@out=$$(GOWORK=off gofmt -l -s .); if [ -n "$$out" ]; then echo "$$out" >&2; exit 1; fi

vet:
	GOWORK=off go vet .

# preflight = gofmt + vet + build + test — the CI gate.
# lint is NOT in preflight (errcheck -blank flags 100+ test-side _, _ = patterns
# that are idiomatic Go test code). Run `make lint` separately.
# Run locally before pushing: `make preflight`.
preflight: fmt vet build test

clean:
	go clean -cache -testcache

# Follow-up (not blocking): WebSocket protocol conformance via the Autobahn
# Test Suite (https://github.com/crossbario/autobahn-testsuite). Run locally
# with `wstest -m fuzzingclient` against a test-server harness — not wired
# into preflight because it requires a docker/autobahn environment.
