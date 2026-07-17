.PHONY: check fmt vet test test-race run build tidy

STORE_BACKEND ?= memory

check: fmt vet test-race

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test -shuffle=on ./...

test-race:
	go test -race -shuffle=on ./...

run:
	STORE_BACKEND=$(STORE_BACKEND) go run ./cmd/urlshortener

build:
	go build -o bin/urlshortener ./cmd/urlshortener

tidy:
	go mod tidy
