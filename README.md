# URL Shortener

A small, modular Go service that turns long URLs into short codes and serves
`301` redirects. It uses a shared, atomic uniqueness namespace so short codes
**cannot** collide, treats duplicate URLs and custom aliases with deliberate and
documented semantics, and is structured around interface seams so the datastore
and code-generation strategy are swappable.

- **Datastore:** PostgreSQL in production (a `UNIQUE` constraint owns
  uniqueness); an in-memory store for tests and zero-dependency local runs.
- **Codes:** short, URL-safe **base62** of a database sequence id, allocated in
  **blocks** to amortise round trips; optionally Feistel-permuted for opacity.

## Quick start

Requirements: Go 1.25+ (built with 1.26).

**Zero dependencies (in-memory store):**

```bash
go run ./cmd/urlshortener
# listening on http://localhost:8080, store=memory
```

**With PostgreSQL (production path):**

```bash
docker compose up --build     # app + postgres
# or point at your own database:
DATABASE_URL="postgres://user:pass@host:5432/db" go run ./cmd/urlshortener
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
codes.

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
  store/postgres/      PostgreSQL Store + block allocator + idempotent schema
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
| `HTTP_ADDR` | `:8080` | Listen address. |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Trusted origin for `short_url`; request `Host` is never trusted. |
| `DATABASE_URL` | _(empty)_ | If set, use PostgreSQL; otherwise the in-memory store. |
| `BLOCK_SIZE` | `100` | Sequence ids fetched per round trip (Option A). |
| `CODE_OFFSET` | `1000000000` | Starting id for the in-memory allocator (keeps codes ~6 chars). |
| `MAX_RETRIES` | `4` | Bound on the generated-code retry loop. |
| `FEISTEL_KEY` | `0` | If non-zero, generated codes are Feistel-permuted (opaque, non-sequential). |

## Continuous integration

`.github/workflows/ci.yml` runs on every push/PR: `gofmt`, `go vet`, `go mod
tidy` drift check, and `go test -race -shuffle=on ./...`; a second job runs the
PostgreSQL store against a service container (`-tags=integration`); and a Docker
job publishes to GHCR only on a `v*` tag.

`.github/workflows/keploy.yml` runs [Keploy](https://keploy.io) API
record/replay tests: it starts the service (in-memory store), records real HTTP
traffic into test cases, then replays them to catch contract regressions. The
current Keploy CLI requires authentication, so this job needs a repository
secret **`KEPLOY_API_KEY`** (from https://app.keploy.io → API keys). Without the
secret the job skips cleanly. Add it under *Settings → Secrets and variables →
Actions → New repository secret*.

## Scope and next steps

Analytics is intentionally omitted: a `301` can be cached by browsers and
intermediaries, so an origin-side counter would not represent every visit; any
future analytics must be asynchronous and out-of-band. Further work: auth, rate
limits, and abuse controls (the service is an intentional open redirector);
metrics/tracing that avoid destination query strings; and reconsidering
permanent redirects if editable links, expiry, or deletion are added.
