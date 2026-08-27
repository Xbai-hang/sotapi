.PHONY: test coverage vet verify

COVERAGE_MIN_GLOBAL ?= 85
COVERAGE_MIN_CORE ?= 90
CORE_PKGS_PATTERN ?= "internal/(completion|fallback|protocol)"

test:
	go test -race ./...

coverage:
	go test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@COVERAGE_MIN_GLOBAL=$(COVERAGE_MIN_GLOBAL) \
	 COVERAGE_MIN_CORE=$(COVERAGE_MIN_CORE) \
	 CORE_PKGS_PATTERN=$(CORE_PKGS_PATTERN) \
	 ./scripts/check-coverage.sh coverage.out

vet:
	go vet ./...

verify: vet coverage
