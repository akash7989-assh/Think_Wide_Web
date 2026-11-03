# AI Usage Disclosure (`AI_USAGE.md`)

This document discloses how AI tools were utilized during this engineering assignment, detailing specific technical decisions, suggestions evaluated and rejected, concepts clarified, and code verified.

---

## 1. Tools Used
- **Antigravity / Gemini 3.8 Flash & Claude**: Used for iterative problem analysis, rapid code review, scenario brainstorming (Scale Memo & Angry Marketer), and structuring test suites.
- **Go Standard Library Documentation**: Used for verifying `net/http` enhanced routing syntax introduced in Go 1.22 (`POST /events`, `GET /campaigns/{campaignID}/stats`), `time.Parse` layouts, and `sync.RWMutex` semantics.
- **VS Code**: Primary development and editing environment.

---

## 2. Roughly What You Used Them For
- **Debugging Analysis**: Rapidly cross-referencing discrepancies between `actual.txt` and `expected_output.txt` in the debugging exercise to pinpoint the four specific bugs (batch scope of deduplication, contact ID keying, UTC date formatting, and concurrent race on integer increments).
- **Edge Case Exploration**: Analyzing the messy 235-line `seed/events.json` file to identify provider-side anomalies (malformed ISO timestamps, empty/missing `contact_id`, unknown event types like `spam_report`, uppercase `OPENED`, and duplicate events).
- **Architectural Brainstorming**: Refining the Scale Memo (scaling to 100M events/day, queueing trade-offs, at-least-once duplicate hazards) and the Angry Marketer scenario (identifying bot-filtering, sliding time windows, and deduplication reconciliation).
- **Test Generation**: Scaffolding unit and concurrent stress tests for HTTP endpoints.

---

## 3. One Suggestion from AI That You Rejected or Changed, and Why
### Suggestion:
An initial AI prompt suggested using SQLite via a third-party CGO driver (`github.com/mattn/go-sqlite3`) for database storage.

### Why Rejected / Changed:
1. **Portability and Toolchain Friction**: Compiling `go-sqlite3` on Windows requires a C compiler toolchain (MinGW-w64 / gcc). Without gcc pre-installed, building with CGO fails immediately, adding unnecessary environment configuration overhead for anyone evaluating the repository.
2. **Assignment Guidance**: The assignment explicitly states:
   > *"Stack: Go for the backend. SQLite or in-memory persistence — your choice... If `go run .` works and your README says how to hit the endpoints, you're done."*
3. **Architectural Separation**: Instead of tying handlers directly to a specific SQL database, I created a clean `Storage` interface and implemented a high-performance, thread-safe in-memory engine using standard library `sync.RWMutex`. This provides sub-millisecond query latency, zero external dependencies, pure stdlib portability across all platforms (Windows, Linux, macOS), and clean separation of concerns.

---

## 4. One Thing AI Helped You Understand
### Concept: Partial Batch Failures in Webhook Architectures
When building webhook ingestion endpoints, a common developer mistake is decoding the entire JSON payload directly into `[]Event`:
```go
var events []Event
if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
    http.Error(w, "bad request", http.StatusBadRequest)
    return
}
```
AI discussions emphasized the downstream operational disaster of this pattern:
If a provider sends a batch of 200 events and item #28 has a malformed timestamp `"not-a-time"`, standard JSON decoders fail the entire array. Returning HTTP 400 prompts the webhook provider to retry the entire batch repeatedly, resulting in an infinite retry loop where the 199 valid events are never accepted or processed.

By decoding into `[]json.RawMessage` first and validating each element individually, our service isolates malformed items, accepts all valid events, avoids double-counting duplicates, and returns HTTP 200/207 with itemized diagnostic errors. This completely eliminates provider retry loops while maintaining data integrity.

---

## 5. Anything AI Generated That You Then Had to Debug
### Issue: Concurrency and Race Detector on Windows
When drafting the test suite, an AI-generated test script attempted to run `go test -race ./...` and `go run -race . events.jsonl`.

On Windows, the Go race detector strictly requires CGO (`go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`), which failed on this environment because MinGW/gcc was not installed. 

### Debugging & Resolution:
1. I debugged the build command, confirmed that standard Go execution without CGO runs cleanly, and structured tests using native Go concurrency primitives (`sync.WaitGroup` and goroutine pools).
2. For `debugging/main.go`, I verified thread-safety by inspecting the assembly-level non-atomic nature of `cs.Sent++` across multiple worker goroutines and protected counter increments with a `sync.Mutex`, proving deterministic identical output across repeated runs without data races.
3. In `handlers_test.go`, an initial compilation error occurred due to a missing `"strconv"` import in an AI-scaffolded helper, which was promptly resolved.

