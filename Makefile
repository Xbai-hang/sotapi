.PHONY: test coverage vet verify

COVERAGE_MIN ?= 90

test:
	go test -race ./...

coverage:
	go test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@total="$$(go tool cover -func=coverage.out | awk '/^total:/ {gsub("%", "", $$3); print $$3}')"; \
	awk -v total="$$total" -v minimum="$(COVERAGE_MIN)" 'BEGIN { \
		printf "total coverage: %.1f%% (minimum %.1f%%)\n", total, minimum; \
		if (total + 0 < minimum + 0) exit 1 \
	}'

vet:
	go vet ./...

verify: vet coverage
