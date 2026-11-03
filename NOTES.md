# Engineering Notes: Campaign Events Service

This document captures the technical reasoning, architectural decisions, scale considerations, edge-case analyses, and product trade-offs for the Campaign Events assignment.

---

## Part 1 — Read and Think First

### 1. Problem Interpretation
Relay acts as a dispatch platform for marketing communications (email, SMS, push) across multiple client campaigns. Relay does not handle message transport directly; instead, it hands messages over to downstream delivery providers. As providers attempt transmission and recipients engage, providers asynchronously call Relay's webhook with event telemetry (`sent`, `delivered`, `opened`, `clicked`).

Our mission is to build the ingestion and reporting engine for this pipeline:
1. **High-Throughput Webhook Ingestion (`POST /events`)**: Ingest batched events from providers under messy real-world conditions (network retries, duplicate events, out-of-order transitions, late arrivals, and malformed records).
2. **Real-Time Aggregated Statistics (`GET /campaigns/{campaign_id}/stats`)**: Serve real-time, non-double-counted campaign statistics to marketer dashboards with sub-millisecond query latency.

#### Core Invariants
- **Global Idempotency**: An `event_id` is assigned by the provider and identifies one discrete event occurrence. Ingestion must be strictly idempotent: receiving an identical `event_id` multiple times must never inflate counters.
- **Out-of-Order Delivery**: Physical events can arrive in arbitrary order. An `opened` event can arrive before a `delivered` event due to network routing or provider batching delays. Counter aggregation cannot assume linear state progression.
- **Campaign-Scoped Unique Opens**: `unique_opens` measures the count of distinct contacts that opened a message for a specific campaign. If contact `ct_01` opens 5 messages within `cmp_A`, it counts as 1 unique open for `cmp_A`. If `ct_01` also opens a message in `cmp_B`, it also counts as 1 unique open for `cmp_B`.

---

### 2. Assumptions
1. **Deduplication Key & First-Write-Wins**:
   `event_id` is the canonical idempotency key. If an event arrives with an `event_id` that has already been accepted, it is considered a duplicate retry. Even if secondary fields (e.g. metadata) vary slightly on a retry, the initial recorded state is preserved to ensure deterministic consistency.
2. **Partial Batch Fault-Tolerance**:
   Providers send batches containing multiple events. If one event out of 200 contains a corrupt timestamp or missing field, rejecting the entire batch with `400 Bad Request` would trigger provider retry storms for the 199 valid events. Therefore, we parse batches resiliently (`[]json.RawMessage`), validate each record independently, accept valid events, ignore duplicates, and return an itemized summary (`status: partial_success`, HTTP 200/207) indicating rejected records and reasons.
3. **Event Normalization**:
   Event types are normalized to lowercase (`"OPENED"` -> `"opened"`). Valid types are strictly restricted to: `sent`, `delivered`, `opened`, `clicked`. Unknown event types (e.g. `spam_report`) are rejected.
4. **Timestamps**:
   Timestamps reflect provider occurrence times in UTC. Timestamps must follow ISO8601/RFC3339 format. Events with unparseable timestamps (e.g. `"not-a-time"` or non-standard date formats) are rejected.
5. **Required Fields**:
   `event_id`, `campaign_id`, `contact_id`, `type`, and `timestamp` must be non-empty strings. `metadata` is optional.
6. **Unknown Campaign Query**:
   Querying `GET /campaigns/{campaign_id}/stats` for a campaign that has never received any events returns `404 Not Found`, distinguishing non-existent campaigns from zeroed active campaigns.

---

### 3. Ambiguities in the Brief
1. **Duplicate HTTP Response Code**:
   Should a duplicate event return `200 OK`, `201 Created`, or `409 Conflict`? In webhook integrations, providers treat 4xx as an error and continue retrying. Returning `200 OK` with `duplicates: N` in the JSON response signals successful receipt while preventing retry storms.
2. **Payload Conflict on Duplicate Event ID**:
   What happens if a provider reuses an `event_id` for a completely different contact or event type? The brief defines `event_id` as identifying one event. We treat the first write as definitive.
3. **Data Retention & Aggregation Horizon**:
   The brief does not specify how long raw events must be retained, or if old campaigns are archived. In our service, we retain the event log and aggregated counters in memory.

---

### 4. Questions for the Product Manager
1. *When providers send malformed events in a batch, do they inspect HTTP error bodies, or do they retry blindly on non-2xx?*
   (Guides whether we return HTTP 200 with partial error details or HTTP 207 Multi-Status).
2. *Can a marketer view stats restricted to a custom time window (e.g., "last 24 hours" vs "all time")?*
   (If time windows are required, pre-aggregated cumulative counters must be supplemented with time-bucketed indices).
3. *What is the SLA on dashboard freshness? Is sub-second eventual consistency acceptable if traffic spikes to millions of events per hour?*
4. *Do we need to enforce message state progression (e.g. rejecting `clicked` if no `sent` ever existed for that contact)?*

---

### 5. Implementation Priorities & Sequencing
1. **Part 3 (Debugging Exercise)**: First priority. Resolving the 4 concurrency, deduplication, timezone, and unique-open bugs in `debugging/` solidifies our understanding of the core domain logic and establishes ground truth against `expected_output.txt`.
2. **Part 2 (Backend Service)**:
   - Resilient batch parsing (`[]json.RawMessage`) to isolate malformed records.
   - Decoupled storage engine with a clean `Storage` interface.
   - In-memory thread-safe implementation (`sync.RWMutex`) with atomic batch ingestion.
   - Comprehensive validation, normalization, and deduplication.
   - Endpoints: `POST /events`, `GET /campaigns/{campaign_id}/stats`, and optional `GET /campaigns/{campaign_id}/events`.
   - Unit, concurrency, and seed-file survival tests.
3. **Part 4 (Scale Memo)**: Document architecture evolution from 1,000 to 100,000,000 events/day.
4. **Part 5 (The Angry Marketer)**: Deep forensic analysis of declining metrics.
5. **AI Usage & Documentation**: Transparent disclosure in `AI_USAGE.md` and complete setup/run guide in `README.md`.

---

## Part 4 — Scale Memo (100 Million Events / Day)

### 1. What Breaks First in the Current Implementation?
At 100,000,000 events/day (~1,160 events/second average, peaking at 5,000–10,000 events/second):
1. **Memory Exhaustion (OOM)**:
   Storing raw event objects and strings in a single Go process's RAM will exhaust heap memory within days (100M events/day at ~200 bytes/event is ~20 GB raw RAM per day without GC overhead).
2. **Ingestion Lock Contention**:
   The single write lock (`sync.RWMutex.Lock()`) serializes all incoming batch writes. As batch frequency rises, goroutines will block waiting for the lock, increasing HTTP response latency and causing provider timeouts.
3. **Process Volatility**:
   Because storage is in-process memory, restarting the container or deploying a new build loses all state unless replayed.

---

### 2. What Would You Change, and in What Order?
1. **Step 1: Asynchronous Ingestion via Message Queue (Buffer)**:
   Decouple HTTP ingestion from persistence. Webhook handlers validate JSON syntax, write events immediately to a distributed append-only log (e.g. Apache Kafka, AWS Kinesis, or NATS JetStream), and return HTTP 202/200 immediately (<10ms).
2. **Step 2: Partitioned Consumer Pipeline**:
   Partition the queue by `campaign_id` or `contact_id`. Multiple worker instances consume distinct partitions independently without lock contention.
3. **Step 3: Distributed Storage Tier**:
   - Write immutable raw events into an append-only distributed datastore (e.g. ClickHouse or TimescaleDB) partitioned by time and campaign.
   - Maintain fast deduplication state using Redis (Bloom filter + TTL key store) or database primary key constraints (`ON CONFLICT DO NOTHING`).
4. **Step 4: Pre-Aggregated Counter Serving**:
   Maintain campaign rollup counters in Redis / DynamoDB for instantaneous O(1) dashboard reads.

---

### 3. Would the API Contract Change? Would Storage Design Change?
- **API Contract**:
  - `POST /events`: The external contract with providers does **not** need to change. Maintaining backwards compatibility protects against provider re-integration costs. We may add optional idempotency headers (`Idempotency-Key`).
  - `GET /campaigns/{id}/stats`: The contract remains identical, but responses can include a `last_updated_at` timestamp reflecting stream consumer progress.
- **Storage Design**:
  - Transition from in-memory maps to a hybrid OLTP/OLAP architecture:
    - **OLAP / Ingestion Log**: ClickHouse for event log storage and time-series analytical queries.
    - **Fast Cache / Counters**: Redis / Key-Value store for real-time campaign counters and contact deduplication sets (using HyperLogLog or Redis Sets).

---

### 4. Introducing a Queue: What New Problems Does That Create?
- **Eventual Consistency**:
  The dashboard no longer sees events instantaneously. There is a processing lag (typically 50ms–2s) between provider POST and stat updates.
- **Consumer Failures & Backpressure**:
  If workers crash or database writes stall, queue consumer lag accumulates. Queue storage and alerts are necessary.
- **Out-of-Order Message Processing**:
  If partitioned by `event_id` instead of `campaign_id`, events for the same campaign/contact could be processed out of order across workers. (Solved by partitioning by `campaign_id` or `contact_id`).

---

### 5. Where Can Duplicates Sneak In That Couldn't Before?
1. **At-Least-Once Queue Semantics**:
   Message brokers guarantee at-least-once delivery. If a consumer processes an event, increments a counter, and crashes before committing the queue offset, the message is redelivered to another consumer.
2. **Network Retries between Ingestion and Queue**:
   If the webhook service writes to Kafka but the network ACK drops, the producer retries, writing the same event twice into the queue.
3. **Distributed Deduplication Races**:
   If two identical events arrive at different ingestion nodes simultaneously, both might query a deduplication cache before either writes, resulting in double processing unless atomic checks (`SET NX` in Redis or DB unique index) are enforced.

---

### 6. How to Keep Counters Trustworthy?
- **Atomic Idempotent Consumption**:
  Use atomic deduplication keys in storage (`INSERT INTO events (...) ON CONFLICT (event_id) DO NOTHING`). Only if the insert succeeds is the counter incremented.
- **Event Sourcing / Periodic Reconciliation**:
  Treat the raw event log in ClickHouse/S3 as the single source of truth. Run periodic scheduled reconciliation jobs (e.g. hourly cron) that recompute counts (`SELECT campaign_id, COUNT(DISTINCT contact_id)...`) and reconcile any drift in the live cached counters.

---

### 7. What to Monitor?
1. **Ingestion Metrics**: HTTP `POST /events` throughput (RPS), p95/p99 latency, HTTP 4xx/5xx error rates.
2. **Queue Health**: Queue depth / consumer lag in seconds and message count.
3. **Worker Processing**: Consumer throughput, error rate, dead-letter queue (DLQ) rate.
4. **Data Integrity Metrics**: Duplicate rate percentage (`duplicates / total_received`), rejected malformed payload rate.
5. **Dashboard Latency**: `GET /campaigns/{id}/stats` response time (target <25ms p99).

---

### 8. What Deliberately NOT to Solve Yet?
- **Multi-region active-active database replication**: Over-engineering before single-region throughput limits are reached.
- **Complex real-time streaming topologies (e.g. Apache Flink)**: A simple Go consumer group reading Kafka and writing to ClickHouse/Redis handles 100M events/day (~1,200 RPS) comfortably without Flink operational overhead.
- **Custom ad-hoc SQL query builders for marketers**: Stick to pre-aggregated dashboard endpoints.

---

## Part 5 — The Angry Marketer Scenario

### The Situation
- **At 10:00**:
  `Sent: 1,000,000 | Delivered: 970,000 | Opened: 250,000 | Clicked: 20,000`
- **At 10:30**:
  `Sent: 1,000,000 | Delivered: 975,000 | Opened: 248,000 | Clicked: 20,000`

**Delivered went UP (+5,000). Opened went DOWN (-2,000).**
The marketer files a ticket: *"Your dashboard is broken."*

---

### 1. Plausible Explanations Before Assuming Code is Broken
Before concluding there is a software bug, consider these operational and domain realities:
1. **Privacy Protection & Bot Filtering (Retroactive Correction)**:
   Modern email security appliances and privacy systems (e.g., Apple Mail Privacy Protection, corporate security scanners) pre-fetch images/links upon receipt to scan for malware, firing automated `opened` events. Upstream providers (or Relay's bot-detection engine) retroactively classify these automated interactions as non-human bots and issue retraction/correction events or filter them out during periodic aggregation.
2. **Recipient Spam Complaints / Unsubscribes / Data Deletion (GDPR/CCPA)**:
   If recipients marked messages as spam or exercised "right to be forgotten" between 10:00 and 10:30, their historical interactions and contact IDs may have been purged or excluded from active reporting.
3. **Dashboard Reporting Time Windows & Filter Shifts**:
   The marketer may have changed their dashboard filter between 10:00 and 10:30:
   - Switching from "All Time" to "Today (UTC)", "Last 7 Days", or a specific timezone.
   - Filtering by segment, device, or geographic region.
   - A sliding window filter (e.g. "Last 24 Hours") where 2,000 opens that occurred yesterday morning rolled out of the 24-hour window between 10:00 and 10:30.
4. **Reconciliation Job / Materialized View Refresh**:
   If the 10:00 snapshot was reading from an un-deduplicated real-time cache (which counted raw opens or had a temporary race), and a periodic exact reconciliation job ran at 10:15 that accurately counted `COUNT(DISTINCT contact_id)`, the number corrected downward to the true count.
5. **Multi-Node Read Replica Lag / Cache Inconsistency**:
   If the marketer's 10:00 request hit one cache/database replica and the 10:30 request hit a different replica that was lagging or had a stale cache entry, inconsistent values could be observed.

---

### 2. Is the Delivered Number Rising Actually Suspicious?
**No, delivered rising is completely normal and expected.**
- Message delivery is asynchronous. Email, SMS, and push gateways queue messages and retry temporary failures (e.g. greylisting, mailbox full, network throttling) over hours or even days.
- Receiving 5,000 additional delivery confirmations between 10:00 and 10:30 simply means the provider successfully completed deliveries for previously deferred messages.
- Furthermore, events arrive out of order: an open could have been recorded earlier, while the confirmation of delivery arrived later.

---

### 3. What to Check First, and How to Decide?
To definitively determine whether this is expected behavior or a bug, execute the following triage steps in order:

1. **Check Audit Logs for Request Parameters**:
   Inspect the access logs for both dashboard requests (10:00 and 10:30). Did the HTTP query parameters match exactly (campaign ID, date range, timezone, user segment)?
2. **Query the Immutable Event Log**:
   Run an exact count directly on the immutable event store for that campaign ID:
   ```sql
   -- Total opens recorded
   SELECT COUNT(*) FROM events WHERE campaign_id = 'cmp_xyz' AND type = 'opened';
   -- Unique contacts that opened
   SELECT COUNT(DISTINCT contact_id) FROM events WHERE campaign_id = 'cmp_xyz' AND type = 'opened';
   ```
   Compare the database truth against the 250,000 and 248,000 numbers.
3. **Inspect Deletions and Bot Classification**:
   Check if any data deletion events, deduplication reconciliations, or anti-bot filtering jobs ran between 10:00 and 10:30.
4. **Check Cache / Aggregation Engine State**:
   Verify whether Redis counter keys or materialized views experienced a reset, key eviction, or out-of-sync replica read.

**Decision Matrix**:
- If query logs show a filter difference or a bot-scrubbing run: **Expected behavior**; provide the marketer with an explanation of bot filtration or filter adjustments.
- If the immutable event count has always been 248,000 and the 10:00 count was inflated by duplicate counting in the cache: **Bug in real-time cache deduplication**.
- If events were erroneously deleted or undercounted due to a code regression: **Backend aggregation bug**.

---

## What I Completed / What I Intentionally Skipped

### What I Completed:
1. **Part 1 & Domain Design**: Documented core interpretation, assumptions, ambiguities, PM questions, and priorities.
2. **Part 3 (Debugging Exercise)**: Diagnosed and surgically fixed all four bugs in `debugging/main.go` (cross-batch deduplication, campaign-scoped unique opens, UTC bucket formatting, and worker counter synchronization). Verified byte-for-byte identity against `expected_output.txt` across repeated runs.
3. **Part 2 (HTTP Backend Service)**: Built a production-ready Go HTTP service in `backend/` and `starter/`:
   - Resilient item-by-item batch JSON decoding via `[]json.RawMessage`.
   - Strict field validation, timestamp parsing, and event type normalization.
   - Clean, decoupled `Storage` interface and thread-safe in-memory engine.
   - Atomic batch ingestion with global `event_id` deduplication and contact-level unique open tracking.
   - Implemented required endpoints `POST /events` and `GET /campaigns/{id}/stats`.
   - Implemented optional paginated event history endpoint `GET /campaigns/{id}/events`.
   - Full test suite passing 100%, including concurrent ingestion stress tests and full survival of the messy 235-line `seed/events.json` provider file.
4. **Part 4 (Scale Memo)**: Completed detailed 100M events/day architecture, bottlenecks, queueing trade-offs, and monitoring strategy.
5. **Part 5 (The Angry Marketer)**: Completed forensic analysis of decreasing open metrics and triage strategy.
6. **AI Usage & Documentation**: Comprehensive `AI_USAGE.md`, `BUGS.md`, and `README.md`.

### What I Intentionally Skipped:
1. **External Database (SQLite / Postgres)**: Used an in-memory thread-safe storage engine behind a clean `Storage` interface. This avoided CGO dependencies on Windows, ensured 100% stdlib portability, and eliminated external setup friction while fulfilling all assignment requirements.
2. **Distributed Infrastructure (Kafka / Redis / Docker)**: Kept the submission focused, lightweight, and directly runnable via `go run .` and `go test ./...` as recommended in the brief.

---

## If I Had Another Day
1. **Persistent SQLite / BoltDB Storage Driver**:
   Implement a second backend implementing the `Storage` interface using pure-Go SQLite (`modernc.org/sqlite`) or embedded key-value storage (`bbolt`) to persist records across server restarts without external daemon requirements.
2. **Dynamic Time-Window Aggregations**:
   Add hourly and daily bucket indices so dashboard clients can query arbitrary time ranges (`?from=...&to=...`) rather than only all-time campaign totals.
3. **Open-to-Click Ratio & Funnel Metrics**:
   Enrich the stats endpoint with computed conversion rates (`delivery_rate`, `open_rate`, `click_to_open_rate`).
4. **Benchmarking & Memory Profiling Suite**:
   Add Go benchmark tests (`go test -bench=. -benchmem`) to profile allocations during high-volume JSON decoding and measure peak throughput.

