# Campaign Events Engineering Assignment

A high-performance, fault-tolerant event ingestion and statistics reporting service built with Go for Relay's marketing campaign pipeline.

---

## Repository Structure

```
.
├── ASSIGNMENT.md         # Original assignment specification
├── NOTES.md              # Part 1 (Read & Think), Part 4 (Scale Memo), Part 5 (Angry Marketer)
├── BUGS.md               # Part 3 (Analysis of four bugs in the debugging program)
├── AI_USAGE.md           # Transparent disclosure of AI tool usage
├── README.md             # This document: architecture, run instructions, and test guide
│
├── debugging/            # Part 3: Fixed debugging program
│   ├── main.go           # Surgical fixes applied (4 bugs fixed)
│   ├── events.jsonl      # 20,000-line test dataset
│   ├── expected_output.txt # Exact expected ground truth stdout
│   └── README.md
│
├── backend/              # Part 2: Production-grade Go HTTP Service
│   ├── main.go           # Server startup and configuration
│   ├── models.go         # Domain models, event normalization, and validation logic
│   ├── storage.go        # Thread-safe in-memory storage engine behind Storage interface
│   ├── handlers.go       # HTTP handlers with resilient JSON batch parsing
│   ├── handlers_test.go  # Comprehensive unit, concurrency, and seed data test suite
│   ├── go.mod            # Go 1.22 module (stdlib only, zero third-party dependencies)
│   └── seed/
│       └── events.json   # 235-line messy provider traffic seed file
```

---

## Quick Start: Prerequisites

- **Go 1.22 or higher** installed (`go version`).
- **No external dependencies** or database installations required (100% standard library).

---

## Part 3: Running the Debugging Exercise

The debugging exercise processes 20,000 JSONL events and computes per-campaign statistics. All four bugs (cross-batch deduplication, contact ID keying, UTC date formatting, and concurrent race on integer increments) have been fixed.

To execute and verify against ground truth:

```bash
cd debugging
go run . events.jsonl > actual.txt
```

### Compare Output with Ground Truth:
- **On Linux / macOS**:
  ```bash
  diff actual.txt expected_output.txt
  # Output is empty — 100% byte-for-byte match!
  ```
- **On Windows (PowerShell / Command Prompt)**:
  ```cmd
  fc actual.txt expected_output.txt
  # Output: FC: no differences encountered
  ```

For detailed root-cause analysis and verification of the four bugs, see [BUGS.md](file:///C:/Users/haree/Downloads/TWW%20Main%20Task/BUGS.md).

---

## Part 2: Running the Backend Service

### 1. Start the HTTP Server

```bash
cd backend
go run .
```

The server listens on `:8080` (or the port specified by the `PORT` environment variable):

```
Campaign Events backend listening on :8080
Endpoints:
  POST :8080/events
  GET  :8080/campaigns/{campaign_id}/stats
  GET  :8080/campaigns/{campaign_id}/events (paginated)
```

---

### 2. Hitting the API Endpoints

#### A. Ingest Events (`POST /events`)

Accepts a JSON array of webhook events. Supports resilient batch parsing where invalid records are diagnosed without dropping valid events.

```bash
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '[
    {"event_id":"evt_001","campaign_id":"cmp_summer_sale","contact_id":"ct_01","type":"sent","timestamp":"2026-08-10T09:00:00Z"},
    {"event_id":"evt_002","campaign_id":"cmp_summer_sale","contact_id":"ct_01","type":"delivered","timestamp":"2026-08-10T09:01:00Z"},
    {"event_id":"evt_003","campaign_id":"cmp_summer_sale","contact_id":"ct_01","type":"opened","timestamp":"2026-08-10T09:05:00Z"},
    {"event_id":"evt_004","campaign_id":"cmp_summer_sale","contact_id":"ct_01","type":"clicked","timestamp":"2026-08-10T09:10:00Z"}
  ]'
```

**Response (`200 OK`)**:
```json
{
  "status": "success",
  "received": 4,
  "accepted": 4,
  "duplicates": 0,
  "rejected": 0
}
```

---

#### B. Query Campaign Statistics (`GET /campaigns/{campaign_id}/stats`)

Returns aggregated statistics for a campaign, including total sent, delivered, opened, clicked, and unique contacts who opened.

```bash
curl http://localhost:8080/campaigns/cmp_summer_sale/stats
```

**Response (`200 OK`)**:
```json
{
  "campaign_id": "cmp_summer_sale",
  "sent": 1,
  "delivered": 1,
  "opened": 1,
  "clicked": 1,
  "unique_opens": 1
}
```

If the campaign ID does not exist, returns `404 Not Found`:
```json
{
  "error": "campaign not found",
  "campaign_id": "cmp_unknown"
}
```

---

#### C. Ingest the Messy Seed Dataset (`seed/events.json`)

The `seed/events.json` file contains 235 lines of realistic, messy provider traffic (retries, invalid timestamps, missing contact IDs, unknown types, and casing differences). The service survives 100% of it:

```bash
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  --data-binary @seed/events.json
```

**Response (`200 OK`)**:
```json
{
  "status": "partial_success",
  "received": 235,
  "accepted": 211,
  "duplicates": 18,
  "rejected": 6,
  "errors": [
    {"index": 28, "event_id": "evt_00212", "reason": "invalid timestamp format: cannot parse 'not-a-time' as RFC3339 timestamp"},
    {"index": 60, "event_id": "evt_00211", "reason": "invalid timestamp format: cannot parse '10-08-2026 09:00' as RFC3339 timestamp"},
    {"index": 82, "reason": "missing or empty event_id"},
    {"index": 128, "event_id": "evt_00213", "reason": "missing or empty contact_id"},
    {"index": 170, "event_id": "evt_00214", "reason": "missing or empty contact_id"},
    {"index": 176, "event_id": "evt_00215", "reason": "invalid event type 'spam_report'"}
  ]
}
```

Then query the updated campaign statistics:
```bash
curl http://localhost:8080/campaigns/cmp_summer_sale/stats
```

---

#### D. Query Paginated Event Activity (`GET /campaigns/{campaign_id}/events`) *(Optional Endpoint)*

Returns paginated raw event activity for a campaign:

```bash
curl "http://localhost:8080/campaigns/cmp_summer_sale/events?limit=2&offset=0"
```

**Response (`200 OK`)**:
```json
{
  "campaign_id": "cmp_summer_sale",
  "total": 115,
  "limit": 2,
  "offset": 0,
  "events": [
    {
      "event_id": "evt_00204",
      "campaign_id": "cmp_summer_sale",
      "contact_id": "ct_030",
      "type": "opened",
      "timestamp": "2026-08-10T12:45:00Z"
    },
    {
      "event_id": "evt_00066",
      "campaign_id": "cmp_summer_sale",
      "contact_id": "ct_028",
      "type": "sent",
      "timestamp": "2026-08-10T07:22:00Z"
    }
  ]
}
```

---

## Running the Automated Test Suite

The test suite validates valid ingestion, deduplication, campaign-scoped unique opens, out-of-order delivery, partial batch error handling, 404 responses, pagination, concurrent request ingestion safety, and full survival of the messy seed file.

```bash
cd backend
go test -v ./...
```

Expected output:
```
=== RUN   TestPostEvents_ValidBatch
--- PASS: TestPostEvents_ValidBatch (0.00s)
=== RUN   TestPostEvents_Deduplication
--- PASS: TestPostEvents_Deduplication (0.00s)
=== RUN   TestPostEvents_UniqueOpensPerCampaign
--- PASS: TestPostEvents_UniqueOpensPerCampaign (0.00s)
=== RUN   TestPostEvents_PartialBatchFaultTolerance
--- PASS: TestPostEvents_PartialBatchFaultTolerance (0.00s)
=== RUN   TestGetStats_NotFound
--- PASS: TestGetStats_NotFound (0.00s)
=== RUN   TestGetEvents_Pagination
--- PASS: TestGetEvents_Pagination (0.00s)
=== RUN   TestPostEvents_Concurrency
--- PASS: TestPostEvents_Concurrency (0.00s)
=== RUN   TestSeedDataFile_FullSurvival
    handlers_test.go:266: Seed ingestion results: received=235, accepted=211, duplicates=18, rejected=6
    handlers_test.go:289: cmp_summer_sale stats: {CampaignID:cmp_summer_sale Sent:40 Delivered:35 Opened:28 Clicked:12 UniqueOpens:20}
--- PASS: TestSeedDataFile_FullSurvival (0.00s)
PASS
ok  	campaign-events	...
```

---

## Architectural & Design Highlights

1. **Resilient Item-by-Item Batch Ingestion**:
   Incoming request bodies are decoded as `[]json.RawMessage`. Each event is parsed and validated independently. Malformed records are isolated and logged without failing the entire batch, eliminating provider retry storms.
2. **Global Idempotency & Deduplication**:
   Every incoming `event_id` is verified against an indexed registry. Retried events are identified and skipped, guaranteeing that statistics counters never double-count.
3. **Thread Safety & High Concurrency**:
   Protected by `sync.RWMutex`, allowing non-blocking concurrent reads while ensuring serialized, atomic batch ingestion.
4. **Decoupled Storage Interface**:
   Domain logic and HTTP handlers depend on the `Storage` interface, allowing plug-and-play transitions between in-memory storage, SQLite, PostgreSQL, or ClickHouse without touching HTTP routing logic.
5. **Campaign-Scoped Contact Deduplication**:
   Tracks unique contact opens independently per campaign (`campaign_id` -> `contact_id`), ensuring accurate `unique_opens` metrics.

---

## Documentation Links

- **[NOTES.md](file:///C:/Users/haree/Downloads/TWW%20Main%20Task/NOTES.md)**: Interpretation, Assumptions, Ambiguities, PM Questions, Scale Memo (100M events/day), Angry Marketer Forensic Analysis, and Retrospective.
- **[BUGS.md](file:///C:/Users/haree/Downloads/TWW%20Main%20Task/BUGS.md)**: Detailed diagnosis, code fixes, and verification for all 4 bugs in `debugging/`.
- **[AI_USAGE.md](file:///C:/Users/haree/Downloads/TWW%20Main%20Task/AI_USAGE.md)**: Transparent disclosure of AI tool usage.

