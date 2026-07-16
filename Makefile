GO ?= go

.PHONY: run build test test-race vet fmt fmt-check check

run:
	$(GO) run ./cmd/urlshortener

build:
	mkdir -p bin
	$(GO) build -trimpath -o bin/urlshortener ./cmd/urlshortener

test:
	$(GO) test -shuffle=on ./...

test-race:
	$(GO) test -race -shuffle=on ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@files="$$(find . -type f -name '*.go' -exec gofmt -l {} +)"; \
		test -z "$$files" || (echo "Go files need formatting:"; echo "$$files"; exit 1)

check: fmt-check vet test-race
