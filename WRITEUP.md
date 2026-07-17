## Note: THIS IS THE FILE I GENERATED USING AI with the docs i maintained while using claude: https://docs.google.com/document/d/1PP1IyrPMa7cGMz1tzy6z9xEI7YiYrArxDRzqK2VbZAQ/edit?usp=sharing to develop this project, consult it to know more about my choices and understanding 

### 1. What did you ask the AI to do, and what did you write or decide yourself?
I used AI (Claude and Codex) primarily for scaffolding and initial design. I asked Claude to generate the High-Level Design (HLD), outline the read/write flows, and lay out a modular Low-Level Design (LLD) in Go. It also generated the initial unit tests and boilerplate implementation. 

However, the core architectural decisions and final logic were my own. I decided to use Postgres over embedded KV stores (like SQLite/Badger) to ensure a single source of truth for multi-instance scaling and to leverage `bigserial` sequences. I also designed the custom alias collision logic, integrated Keploy (based on my past experience) to generate integration tests, and set up the CI/CD pipeline. Finally, I directed Codex to review the codebase and fix critical edge cases it uncovered.


### 2. Where did you override, correct, or throw away the AI's output - and why?
I heavily steered the AI during the write-path design and code generation:
*   **Write Path Race Conditions:** Claude initially suggested a "lookup then insert" pattern. I threw this out because it introduces race conditions. It then suggested an optimistic insert using a Postgres `RETURNING + xmax` trick, and later an `ON CONFLICT DO SELECT` clause. I doubted these claims, pushed back, and forced it to verify against actual Postgres documentation. It admitted it had hallucinated the `xmax` trick and that `DO SELECT` wasn't production-ready. I corrected it to use the standard, safe `INSERT... ON CONFLICT(code) DO NOTHING RETURNING`.
*   **Code Generation & Aliases:** Claude initially suggested a random library, which I rejected to avoid retry loops. When it suggested Postgres sequences, it warned that generated codes might collide with custom aliases and suggested rejecting aliases that "look" generated. I overrode this: rejecting useful aliases defeats the purpose of custom links. Instead, I decided that if a generated sequence collides with an existing alias, we simply adjust the sequence and retry. 
*   **Generated Columns:** Claude suggested using a `GENERATED ALWAYS AS (base62(id)) STORED` column. I realized this would reject user-supplied custom aliases, so I threw the idea out.
*   **Code Review Fixes:** Codex caught two high-priority flaws I missed: the app allowed 8KiB URLs but Postgres B-tree indexes limit keys to ~2,700 bytes (causing HTTP 500s), and an empty `DATABASE_URL` silently defaulted to memory storage. I directed Codex to fix both.

### 3. The two or three biggest trade-offs you made, and the alternatives you considered.
*   **Postgres vs. Embedded KV (SQLite/Badger):** 
    *   *Alternative:* SQLite/Badger are read-optimized and simpler for single-node setups. 
    *   *Decision:* Postgres. *Why:* I needed a centralized datastore to support multiple app instances without conflicting counters, and I wanted to use native sequences for ID generation.
*   **Sequence-based IDs vs. Random/Hash-based IDs:**
    *   *Alternative:* Random string generation (requires DB checks) or URL hashing (deterministic but results in longer codes).
    *   *Decision:* Postgres sequences mapped to Base62. *Why:* It guarantees no collisions on the happy path, requires no retry blocks for standard requests, and yields the shortest possible code length.
*   **Full URL Storage vs. URL Digest:**
    *   *Alternative:* Storing the full URL string (up to 8KiB).
    *   *Decision:* Because of the Postgres B-tree limit (~2,700 bytes), storing high-entropy URLs fails. I chose to lower the application validation limit to match the integration-tested DB size, ensuring consistency between the memory store and Postgres.

### 4. What's missing, or what you'd do with another day?
* Use Redis as cache
* Add rate limiting to userID or ip address
* Add feature so that users can authenticate and add/update/delete a link, this would also mean moving to 302 status codes in read path. 