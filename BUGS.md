# Bug Analysis & Resolution: Debugging Exercise (`debugging/`)

This document details the four bugs identified and resolved in `debugging/main.go`. Each entry explains what was wrong, the underlying cause in the Go runtime/codebase, the minimal surgical fix applied, and how the fix was verified.

---

## Bug 1: Batch-Scoped Deduplication (Missing Global Deduplication)

### 1. What the bug is
The program failed to deduplicate events across batches. An `event_id` appearing in an earlier batch of 200 events was processed again if it reappeared in any subsequent batch.

### 2. Why it happens
In `processBatch()`, the map tracking seen event IDs was re-instantiated locally at the start of each batch:
```go
seen := make(map[string]bool) // Allocated per-batch!
```
When `main()` processes a 20,000-line file in batches of 200, each batch began with an empty `seen` map. Any provider retries or duplicate events that crossed batch boundaries were treated as new events, inflating all event counters (`sent`, `delivered`, `opened`, `clicked`).

### 3. Your fix (make minimal changes)
Moved `seen` to package-level scope (`var seen = map[string]bool{}`) alongside `stats` and `openedBy`. Because `processBatch()` is invoked sequentially on the main goroutine, a single persistent map tracks all processed `event_id`s across the entire file. Removed the local `seen := make(...)` inside `processBatch()`.

### 4. How you verified the fix
Checked intermediate event counts before and after the fix against duplicate event IDs in `events.jsonl`. Compared output with `expected_output.txt`, confirming that counts no longer inflate over multiple batches.

---

## Bug 2: Global Contact Deduplication for Unique Opens

### 1. What the bug is
`unique_opens` undercounted unique contacts across distinct campaigns. If a contact opened an email in `cmp_A` and also opened an email in `cmp_B`, `cmp_B`'s `unique_opens` counter was not incremented.

### 2. Why it happens
The specification requires:
> "`unique_opens` = the number of distinct contacts that opened, per campaign. One contact opening five times in a campaign counts once. The same contact opening in two campaigns counts once *in each*."

In `track()`:
```go
case "opened":
    if !openedBy[ev.ContactID] {
        openedBy[ev.ContactID] = true
        cs.UniqueOpens++
    }
```
`openedBy` only used `ev.ContactID` as its key. This tracked unique contacts globally across the whole dataset instead of partitioned by campaign. Once a contact ID opened any campaign, it was marked true forever, suppressing counts for all subsequent campaigns opened by that contact.

### 3. Your fix (make minimal changes)
Updated the deduplication key to composite `CampaignID:ContactID`:
```go
case "opened":
    key := ev.CampaignID + ":" + ev.ContactID
    if !openedBy[key] {
        openedBy[key] = true
        cs.UniqueOpens++
    }
```

### 4. How you verified the fix
Compared `unique_opens` in `actual.txt` against `expected_output.txt`. The previously suppressed unique opens (e.g., `cmp_A unique_opens=942` vs the faulty `549`) matched the expected values exactly.

---

## Bug 3: Timezone Conversion in Daily Delivery Buckets (`Local()` vs `UTC()`)

### 1. What the bug is
The daily delivery buckets grouped events by local machine date instead of UTC date. On systems outside UTC (or across daylight saving shifts), events delivered near UTC midnight shifted into the wrong day.

### 2. Why it happens
In `track()`:
```go
case "delivered":
    day := ev.Timestamp.Local().Format("2006-01-02")
    cs.DailyDelivered[day]++
```
The specification explicitly states:
> "Daily `delivered` buckets use the event's UTC date. The `timestamp` field is UTC."

Calling `ev.Timestamp.Local()` transforms the UTC timestamp to the system's local timezone (for example, UTC+05:30 on this workstation). An event occurring at `2026-08-01 22:30:00 UTC` was converted to `2026-08-02 04:00:00 IST`, incorrectly placing it in the August 2nd bucket instead of August 1st.

### 3. Your fix (make minimal changes)
Changed `.Local()` to `.UTC()`:
```go
case "delivered":
    day := ev.Timestamp.UTC().Format("2006-01-02")
    cs.DailyDelivered[day]++
```

### 4. How you verified the fix
Ran `go run . events.jsonl > actual.txt` and checked the daily delivery lines for every campaign against `expected_output.txt`. Every bucket count matched ground truth identically.

---

## Bug 4: Data Race on Counter Increments (`apply()`)

### 1. What the bug is
The program had a concurrent data race when updating campaign counters (`cs.Sent++`, `cs.Delivered++`, `cs.Opened++`, `cs.Clicked++`).

### 2. Why it happens
In `processBatch()`, a pool of 8 worker goroutines was spawned to process events from the `jobs` channel:
```go
for i := 0; i < numWorkers; i++ {
    wg.Add(1)
    go func() {
        defer wg.Done()
        for ev := range jobs {
            apply(ev)
        }
    }()
}
```
In `apply(ev)`:
```go
func apply(ev Event) {
    cs := stats[ev.CampaignID]
    switch ev.Type {
    case "sent":
        cs.Sent++
    case "delivered":
        cs.Delivered++
    case "opened":
        cs.Opened++
    case "clicked":
        cs.Clicked++
    }
}
```
Even though the `stats` map itself is not mutated (pointers to `CampaignStats` are pre-initialized), multiple workers concurrently processed events sharing the same `CampaignID`. Plain integer increments (`cs.Sent++`, etc.) are non-atomic read-modify-write operations at the CPU / assembly level. Under concurrency, concurrent updates result in race conditions, lost updates, and non-deterministic totals across runs.

### 3. Your fix (make minimal changes)
Introduced a `sync.Mutex` (`var applyMu sync.Mutex`) protecting counter increments in `apply()`:
```go
var applyMu sync.Mutex

func apply(ev Event) {
    applyMu.Lock()
    defer applyMu.Unlock()
    cs := stats[ev.CampaignID]
    switch ev.Type {
    case "sent":
        cs.Sent++
    case "delivered":
        cs.Delivered++
    case "opened":
        cs.Opened++
    case "clicked":
        cs.Clicked++
    }
}
```

### 4. How you verified the fix
1. Ran repeated runs (`go run . events.jsonl`) verifying 100% deterministic, identical output across runs.
2. Verified that counter updates are fully serialized and thread-safe.
3. Verified output against `expected_output.txt` using file comparison (`fc.exe`), with zero differences encountered.

