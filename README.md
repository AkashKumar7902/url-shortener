# URL Shortener

A small, modular Go service that turns long URLs into short codes and serves
`301` redirects. It uses a shared, atomic uniqueness namespace so short codes
**cannot** collide, treats duplicate URLs and custom aliases with deliberate and
documented semantics, and is structured around interface seams so the datastore
and code-generation strategy are swappable.

- **Datastore:** PostgreSQL in production (a fixed-width SHA-256 `UNIQUE` index
  owns exact-URL uniqueness); an explicitly selected in-memory store for tests
  and zero-dependency local runs.
- **Codes:** short, URL-safe **base62** of a database sequence id, allocated in
  **blocks** to amortise round trips; optionally Feistel-permuted for opacity.

## Quick start

Requirements: Go 1.25+ (built with 1.26).

**Zero dependencies (in-memory store):**

```bash
STORE_BACKEND=memory go run ./cmd/urlshortener
# or: make run
# listening on http://localhost:8080, store=memory
```

Memory mode is process-local and non-durable, so it must be selected explicitly.
The service never falls back to it when production configuration is missing.

**With PostgreSQL (production path):**

```bash
docker compose up --build     # app + postgres
# or point at your own database:
STORE_BACKEND=postgres \
DATABASE_URL="postgres://user:pass@host:5432/db" \
go run ./cmd/urlshortener
```

Create and follow a short link:

```bash
curl -i -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/articles?id=42"}' \
  http://localhost:8080/shorten

# Use the returned code. Omit -L to inspect the 301.
curl -i http://localhost:8080/<code>
```

Run every check (format, vet, race+shuffle tests):

```bash
make check
```

## API

### `POST /shorten`

`Content-Type: application/json`. Unknown fields, multiple JSON values, and
bodies over 16 KiB are rejected.

```json
{ "url": "https://example.com/docs?lang=en#install" }
```

Optional custom alias:

```json
{ "url": "https://example.com/docs", "custom_alias": "go_docs" }
```

A new mapping returns `201 Created` (with a `Location` header); an idempotent
repeat returns `200 OK`:

```json
{
  "code": "go_docs",
  "short_url": "http://localhost:8080/go_docs",
  "original_url": "https://example.com/docs",
  "created": true
}
```

### `GET /{code}`

A known code returns `301 Moved Permanently` with the destination in `Location`
and `Cache-Control: public, max-age=31536000, immutable` (mappings never change,
so permanent caching is safe). An unknown or syntactically invalid code returns
`404 Not Found` with `Cache-Control: no-store`, so an alias created later is not
negatively cached.

### Status codes

| Status | Meaning |
| --- | --- |
| `200 OK` | Same generated URL, or same alias→URL, already existed. |
| `201 Created` | A generated code or requested alias was stored. |
| `301 Moved Permanently` | A known code redirects to its destination. |
| `400 Bad Request` | Invalid JSON, URL, or alias. |
| `404 Not Found` | Unknown short code. |
| `405 Method Not Allowed` | Wrong method for the route. |
| `409 Conflict` | Requested alias already maps to a different URL. |
| `413 Request Entity Too Large` | JSON body exceeds 16 KiB. |
| `415 Unsupported Media Type` | Body is not `application/json`. |
| `500 Internal Server Error` | Persistence or code generation failed. |

Errors have one stable shape:

```json
{ "error": { "code": "alias_conflict", "message": "custom alias is already in use" } }
```

## Deliberate behaviour

### Duplicate URLs

Automatic shortening is **idempotent by exact URL string**: the same accepted
URL returns its existing generated code (`200`). Equality is byte-for-byte —
scheme/host casing, trailing slash, query ordering, escaping, and fragments are
**not** canonicalized, because rewriting them can change signed URLs or
application semantics. Equivalent spellings may therefore receive different
codes. PostgreSQL indexes a stored 32-byte SHA-256 digest instead of the full
URL, then verifies the retrieved URL exactly. This keeps the B-tree entry fixed
size for accepted URLs up to 8 KiB without allowing a theoretical digest
collision to return another URL's mapping.

### Custom aliases

An alias is a separate intent, keyed on the alias (not the URL):

- Same alias + same URL → idempotent `200`.
- Same alias + different URL → `409`; the existing mapping is never overwritten.
- A different alias for an already-shortened URL is created normally.
- Aliases and generated codes share **one case-sensitive namespace**, and
  aliases are unrestricted (vanity names like `github` are allowed).

### Collision safety

Generated codes are `base62` of a strictly increasing database sequence id, so
two generated codes **cannot** collide by construction — no probabilistic
argument required. The only residual conflict is a generated code equalling an
existing custom alias in the shared namespace; the atomic insert rejects it and
the service redraws (bounded retry). Correctness is the `UNIQUE` constraint, not
the odds.

### URL-safe codes

Both generated codes (`[0-9A-Za-z]`) and custom aliases (`[A-Za-z0-9_-]`) draw
only from RFC 3986 **unreserved** characters, so codes never require
percent-encoding anywhere in a URL.

## Design: request flows

**Write (`POST /shorten`)** — validate (no canonicalization) → branch on intent:
- *Custom alias:* optimistic `INSERT ... ON CONFLICT DO NOTHING`; on conflict,
  read and compare → `201` / `200` / `409`. Race-free, no wasteful write.
- *Generated:* dedup fast-path lookup; else a bounded loop — allocate an id from
  the in-memory block, encode to base62, insert. A row → `201`; no row + a
  generated code now exists for the URL → `200` (URL race, no retry); no row +
  none exists → the code hit an alias → redraw and retry.

**Read (`GET /{code}`)** — validate code syntax cheaply → single primary-key
lookup (cache-frontable) → `301` on hit, `404 no-store` on miss. The read path
**never writes**, which is what keeps redirects cacheable and scalable.

## Architecture

Dependency arrows point inward: `httpapi → shortener → store`. The domain
declares the seams it needs and imports no transport or concrete store.

```
cmd/urlshortener       composition root: config, wiring, server lifecycle
internal/
  shortener/           domain: Service, validation, Store port, CodeGenerator,
                       IDAllocator, Codec, typed errors
  store/memory/        in-process Store (tests, local)
  store/postgres/      PostgreSQL Store + block allocator + serialized bootstrap migrations
  httpapi/             transport: routing, strict decode, error→status mapping
  config/              env → typed Config
  platform/            Clock, Logger adapters
```

Every axis of likely change is an interface, so it can be swapped in one place:

| Change | Swap |
| --- | --- |
| datastore | `shortener.Store` (memory ↔ postgres) |
| code strategy | `CodeGenerator` (sequence ↔ random) |
| id source | `IDAllocator` (block ↔ counter) |
| alphabet / opacity | `Codec` (base62 ↔ Feistel) |
| transport | `internal/httpapi` |

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `STORE_BACKEND` | _(required)_ | `memory` (explicitly non-durable) or `postgres`. |
| `HTTP_ADDR` | `:8080` | Listen address. |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Trusted origin for `short_url`; request `Host` is never trusted. |
| `DATABASE_URL` | _(empty)_ | Required with `STORE_BACKEND=postgres`; rejected with `memory`. |
| `BLOCK_SIZE` | `100` | Sequence ids fetched per round trip (Option A). |
| `CODE_OFFSET` | `1000000000` | Starting id for the in-memory allocator (keeps codes ~6 chars). |
| `MAX_RETRIES` | `4` | Bound on the generated-code retry loop. |
| `FEISTEL_KEY` | `0` | If non-zero, generated codes are Feistel-permuted (opaque, non-sequential). |

On PostgreSQL startup, schema bootstrap is serialized with an advisory lock and
applied transactionally. Existing databases are upgraded from the original
full-URL unique index to a database-generated SHA-256 column; the replacement
keeps the original index name so older binaries cannot recreate the unsafe
full-URL index during a rolling deployment. Adding the stored column and index
is a blocking migration, so schedule a maintenance window when upgrading a
large existing `links` table.

## Continuous integration

`.github/workflows/ci.yml` runs on every push/PR: `gofmt`, `go vet`, `go mod
tidy` drift check, and `go test -race -shuffle=on ./...`; a second job runs the
PostgreSQL store against a service container (`-tags=integration`); and a Docker
job publishes to GHCR only on a `v*` tag.

`.github/workflows/keploy.yml` runs [Keploy](https://keploy.io) API tests using
the standard record-once / replay-in-CI model. The recorded test cases **and
mocks** are committed under `keploy/`, so CI runs only `keploy test`: the
`mocks.yaml` stands in for PostgreSQL (13 Postgres + 2 DNS interactions), so no
database is started. It uses the open-source Keploy binary (`install.sh --oss`),
so **no API key is needed**.

To re-record after an API change, run it locally and commit the result:

```bash
./scripts/keploy_record.sh   # records into keploy/ via Docker (Linux/eBPF)
git add keploy/ && git commit -m "test(keploy): re-record API test cases"
```

