# syntax=docker/dockerfile:1

# --- build stage ---
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, reproducible binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/urlshortener ./cmd/urlshortener

# --- runtime stage ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/urlshortener /urlshortener
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/urlshortener"]
