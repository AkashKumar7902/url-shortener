FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/urlshortener ./cmd/urlshortener

FROM alpine:3.22

RUN addgroup -S app && adduser -S -G app app \
    && mkdir -p /data \
    && chown app:app /data
WORKDIR /app
COPY --from=build /out/urlshortener /app/urlshortener

USER app
ENV HTTP_ADDR=:8080 \
    PUBLIC_BASE_URL=http://localhost:8080 \
    DATA_FILE=/data/links.json
EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["/app/urlshortener"]
