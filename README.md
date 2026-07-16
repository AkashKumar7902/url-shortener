# URL Shortener

A small Go service that creates durable short links and resolves them with the required `301 Moved Permanently` response. It uses only the Go standard library and keeps its behavioral choices explicit: automatic shortening is idempotent by exact URL, while custom aliases are honored as separate intent.

The required account of AI use, corrections, and trade-offs is in [WRITEUP.md](WRITEUP.md).

> This repository is a confidential take-home submission. Please keep it private.

## Quick start

Requirements: Go 1.25 or newer.

```bash
go run ./cmd/urlshortener
```

The server listens on `http://localhost:8080` and creates `./data/links.json` on the first successful write.

Create and follow a short link:

```bash
curl -i \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/articles?id=42"}' \
  http://localhost:8080/shorten

# Use the code returned above. Do not add -L if you want to inspect the 301.
curl -i http://localhost:8080/<code>
```

Run every formatting, vet, unit, integration, concurrency, and race check:

```bash
make check
```

The individual commands are also available:

```bash
go test -shuffle=on ./...
go test -race -shuffle=on ./...
go vet ./...
```

### Docker

```bash
docker compose up --build
```

The Compose volume keeps links across container restarts. `docker compose down -v` removes that stored data as well as the containers.

## API

### `POST /shorten`

`Content-Type` must be `application/json`. Unknown fields, multiple JSON values, and bodies above 16 KiB are rejected.

Automatic code:

```json
{
  "url": "https://example.com/docs?lang=en#install"
}
```

Requested alias:

```json
{
  "url": "https://example.com/docs?lang=en#install",
  "custom_alias": "go_docs"
}
```

A newly created mapping returns `201 Created`; an idempotent repeat returns `200 OK`:

```json
{
  "code": "go_docs",
  "short_url": "http://localhost:8080/go_docs",
  "original_url": "https://example.com/docs?lang=en#install",
  "created": true
}
```

For a new link, the response `Location` header contains `short_url`.

### `GET /{code}`

A known code returns exactly `301 Moved Permanently`, with the stored destination in the `Location` header (Go applies standard escaping if non-ASCII bytes require it). Mappings are immutable, which makes permanent caching safe. An unknown or syntactically invalid code returns `404 Not Found` with `Cache-Control: no-store` so a missing alias is not negatively cached after it is later created.

### Status codes

| Status | Meaning |
| --- | --- |
| `200 OK` | The same generated URL or the same alias-to-URL mapping already existed. |
| `201 Created` | A generated code or requested alias was stored. |
| `301 Moved Permanently` | A known code redirects to its destination. |
| `400 Bad Request` | JSON, URL, or alias input is invalid. |
| `404 Not Found` | The requested short code does not exist. |
| `405 Method Not Allowed` | The path exists for another HTTP method; `Allow` is returned. |
| `409 Conflict` | A requested alias already points to a different URL. |
| `413 Request Entity Too Large` | The JSON body exceeds 16 KiB. |
| `415 Unsupported Media Type` | The request is not `application/json`. |
| `500 Internal Server Error` | Persistence or code generation failed; implementation details are not exposed. |

Errors have one stable shape:

```json
{
  "error": {
    "code": "alias_conflict",
    "message": "custom alias is already in use"
  }
}
```

## Deliberate behavior

### Duplicate URLs

An automatic request for the exact same accepted URL returns its existing generated code (`200`). Equality is byte-for-byte: scheme/host casing, a trailing slash, query ordering, escaping, and fragments are not canonicalized. Rewriting those details can change signed URLs or application semantics.

Custom aliases express a separate intent:

- The same alias and URL is an idempotent `200`.
- The same alias with another URL is a `409`; the old mapping is never overwritten.
- A different alias for an already-shortened URL is created normally.
- Generated codes and custom aliases occupy one case-sensitive namespace.

Aliases are 1–64 characters from `[A-Za-z0-9_-]`. Because routing is method-specific, `shorten` itself is a valid alias for `GET /shorten`; `POST /shorten` remains the creation endpoint.

### URL validation

Accepted destinations must:

- be at most 8 KiB;
- be absolute `http` or `https` URLs with a hostname;
- contain no raw whitespace, control characters, ambiguous backslashes, malformed escapes, or embedded credentials;
- use ASCII URL syntax; Unicode hostnames must use punycode and non-ASCII path/query text must be percent-encoded;
- use a numeric port from 1 through 65535 when a port is present.

Fragments, IPv4/IPv6 hosts, escaped paths, queries, and local/private hosts are accepted. The service never fetches a destination, so DNS or private-IP blocking would not prevent SSRF here; public deployment instead needs an abuse policy for its intentional open-redirect behavior.

### Collision safety

Generated candidates contain 12 bytes (96 bits) from `crypto/rand`, encoded as 16 unpadded base64url characters. The birthday-bound probability of any random collision among one billion candidates is approximately `6.3e-12`, but probability is not the correctness mechanism.

The datastore owns one atomic uniqueness namespace for aliases and generated codes. A colliding generated candidate cannot overwrite a link: the insert fails, a new candidate is generated, and allocation is retried up to eight times. Tests force this path deterministically instead of relying on a flaky “generate many random values” assertion.

## Storage and architecture

The service has three small layers:

1. `internal/shortener` owns URL/alias policy, idempotency, and collision retry.
2. `internal/store/file` owns atomic uniqueness and durable snapshots.
3. `internal/httpapi` owns strict JSON decoding and HTTP status/error mapping.

The datastore keeps a versioned JSON snapshot with immutable records:

```json
{
  "version": 1,
  "links": [
    {
      "code": "go_docs",
      "url": "https://example.com/docs",
      "kind": "custom",
      "created_at": "2026-07-17T04:00:00Z"
    }
  ]
}
```

Within one process, a mutex makes the two invariants linearizable: codes are unique across both kinds, and each exact URL has at most one generated code. A successful mutation is serialized to a same-directory temporary file, flushed, and atomically renamed over the prior snapshot. If the rename succeeds but directory synchronization fails, that store instance becomes unavailable rather than later acknowledging uncertain durability. Loading also validates the version and every persisted invariant rather than trusting disk contents.

Destination URLs can contain sensitive query tokens. The snapshot is created with mode `0600`, but it is not encrypted; filesystem access and backups must be protected accordingly.

This is intentionally a small-service datastore for one process, modest data, and a local POSIX-style filesystem. Each write clones the index, sorts records, and rewrites the file: `O(n log n)` time, `O(n)` additional memory, and `O(n)` full-file I/O. Separate server processes must not share the file, and network filesystems may not provide the same rename/fsync semantics. A horizontally scaled version would move the same uniqueness rules into PostgreSQL constraints/transactions and cache immutable redirect reads separately.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | Listen address passed to Go's HTTP server. |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Trusted origin used to construct `short_url`; request `Host` is never trusted. |
| `DATA_FILE` | `./data/links.json` | Persistent snapshot path; its parent directory is created automatically. |

`PUBLIC_BASE_URL` must be an absolute HTTP(S) origin without credentials, path, query, or fragment. Example:

```bash
HTTP_ADDR=:9000 \
PUBLIC_BASE_URL=https://sho.rt \
DATA_FILE=/var/lib/urlshortener/links.json \
go run ./cmd/urlshortener
```

## Scope and next steps

The assignment title mentions link analytics, but the functional requirements do not define analytics. It is intentionally not invented here. More importantly, the mandated `301` can be cached by browsers and intermediaries, so an origin-side counter would not represent every visit.

For production, the next priorities would be a transactional SQL datastore for multi-process deployment, authentication/rate limits and abuse handling, then metrics/tracing and an asynchronous analytics design with an explicitly defined counting boundary. Editable links, expiry, and deletion would also require reconsidering permanent redirects.

## Design references

- [Go `crypto/rand`](https://pkg.go.dev/crypto/rand) and [`base64.RawURLEncoding`](https://pkg.go.dev/encoding/base64#RawURLEncoding)
- [Go URL parsing](https://pkg.go.dev/net/url#Parse)
- [Go HTTP server and graceful shutdown](https://pkg.go.dev/net/http#Server)
- [RFC 9110: `301 Moved Permanently`](https://www.rfc-editor.org/rfc/rfc9110.html#name-301-moved-permanently)
- [Go race detector](https://go.dev/doc/articles/race_detector) and [fuzzing](https://go.dev/doc/tutorial/fuzz)
