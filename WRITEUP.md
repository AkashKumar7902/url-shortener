# Engineering write-up

## 1. What I asked AI to do, and what I decided

I used Codex as an implementation and review partner, not just autocomplete. My direct instructions were to use Go, take the assignment to completion, and use multiple independent agents to verify it. Codex turned the brief into acceptance criteria, researched primary Go/HTTP sources, proposed competing designs, drafted the service, tests, and docs, then reviewed the result as an evaluator and hostile production reviewer. My role was direction and final ownership, not pretending I typed the implementation unaided.

The final contract makes its judgments explicit: exact-string duplicate identity, immutable mappings, custom aliases as separate intent, 201 for creation versus 200 for a repeat, one case-sensitive code namespace, and a configured public origin rather than the request Host. Collision/concurrency tests reproduce specific interleavings instead of treating random samples as proof. Verification included formatting, vet, shuffled race tests, focused fuzzing, repeated concurrency tests, and a compiled-process restart round-trip.

## 2. Where I overrode or corrected AI output

Architecture agents recommended SQLite or PostgreSQL. After comparing that advice against the 3–4 hour scope, I kept the implementation's versioned atomic file snapshot. That keeps a fresh clone dependency-free and the invariants visible. The README explicitly limits it to one process, modest data, and a local POSIX-style filesystem.

One proposed design normalized hosts, default ports, and paths. The final design rejects that suggestion because conservative rewriting can still change signed URLs, escaping, or application identity. The service validates and preserves input; equivalent spellings may receive different codes.

The independent reviews found incorrect output. Two concurrent requests for the same URL and candidate could make the loser misclassify the winner as an unrelated collision; a repeating generator could then exhaust the retry budget. I accepted the correction in both layers: the store prioritizes the generated-URL invariant, and the service re-reads the winner after a code conflict. A barrier-based race test reproduces that interleaving. Another review found that public origins ending in an empty `?` or `#` produced malformed short URLs; those forms are rejected. Post-rename handling was also changed so a directory-sync failure makes that store instance unavailable rather than letting a retry acknowledge uncertain durability. Raw Unicode and empty-port cases were tightened after edge-case review.

I deliberately left analytics out despite the title. It is not defined in the requirements, and an origin counter would be misleading because the mandated 301 can be cached before later visits reach the service.

## 3. The biggest trade-offs

**File snapshot versus SQL.** The snapshot gives zero dependencies, one-command startup, restart persistence, and single-process linearizability. Its cost is serialized `O(n log n)` writes, full-file I/O, and no safe multi-process sharing. PostgreSQL is the next step for horizontal deployment; SQLite was the middle option.

**Random codes versus a sequence.** Twelve random bytes make codes opaque, but a candidate can repeat. A base62 sequence would be shorter and guarantee unique candidates, but exposes volume and neighboring links. The 96 bits reduce conflict frequency; atomic uniqueness and retry provide correctness.

**Exact identity versus canonicalization.** Exact equality preserves query order and escaping, but equivalent URLs can receive different codes. I preferred that predictable limitation to silently changing a caller's URL.

## 4. What is missing, or what I would do with another day

I would add a transactional PostgreSQL store behind the existing interface, migrations, and multi-instance tests while keeping the file store as a zero-setup demo. Next come broader crash-point/filesystem fault-injection and load tests, then authentication, rate limits, quotas, and abuse controls. Metrics and tracing should avoid destination query strings. I would design analytics asynchronously only after defining whether a “visit” means an origin hit, edge hit, or navigation, and deciding whether a cacheable 301 fits that definition.
