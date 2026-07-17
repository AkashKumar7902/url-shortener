.PHONY: check fmt vet test test-race run build tidy

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
	go run ./cmd/urlshortener

build:
	go build -o bin/urlshortener ./cmd/urlshortener

tidy:
	go mod tidy
