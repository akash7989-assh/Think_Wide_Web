package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
)

func setupTestServer() (*Server, *MemoryStorage) {
	store := NewMemoryStorage()
	srv := NewServer(store)
	return srv, store
}

func TestPostEvents_ValidBatch(t *testing.T) {
	srv, _ := setupTestServer()

	payload := `[
		{"event_id":"evt_1","campaign_id":"cmp_alpha","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T09:00:00Z"},
		{"event_id":"evt_2","campaign_id":"cmp_alpha","contact_id":"ct_1","type":"delivered","timestamp":"2026-08-10T09:01:00Z"},
		{"event_id":"evt_3","campaign_id":"cmp_alpha","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T09:05:00Z"},
		{"event_id":"evt_4","campaign_id":"cmp_alpha","contact_id":"ct_1","type":"clicked","timestamp":"2026-08-10T09:10:00Z"}
	]`

	req := httptest.NewRequest("POST", "/events", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var res BatchIngestResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if res.Accepted != 4 || res.Duplicates != 0 || res.Rejected != 0 {
		t.Errorf("expected 4 accepted, 0 dupes, 0 rejected; got %+v", res)
	}
}

func TestPostEvents_Deduplication(t *testing.T) {
	srv, _ := setupTestServer()

	payload := `[
		{"event_id":"evt_dup_1","campaign_id":"cmp_alpha","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T09:00:00Z"}
	]`

	// First post
	req1 := httptest.NewRequest("POST", "/events", bytes.NewBufferString(payload))
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, req1)

	var res1 BatchIngestResult
	_ = json.Unmarshal(w1.Body.Bytes(), &res1)
	if res1.Accepted != 1 || res1.Duplicates != 0 {
		t.Fatalf("first batch expected 1 accepted, got %+v", res1)
	}

	// Retry exact same event
	req2 := httptest.NewRequest("POST", "/events", bytes.NewBufferString(payload))
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	var res2 BatchIngestResult
	_ = json.Unmarshal(w2.Body.Bytes(), &res2)
	if res2.Accepted != 0 || res2.Duplicates != 1 {
		t.Fatalf("second batch expected 0 accepted, 1 duplicate; got %+v", res2)
	}

	// Verify stats count is 1, not 2
	statsReq := httptest.NewRequest("GET", "/campaigns/cmp_alpha/stats", nil)
	wStats := httptest.NewRecorder()
	srv.ServeHTTP(wStats, statsReq)

	var stats CampaignStats
	_ = json.Unmarshal(wStats.Body.Bytes(), &stats)
	if stats.Sent != 1 {
		t.Errorf("expected sent=1, got %d", stats.Sent)
	}
}

func TestPostEvents_UniqueOpensPerCampaign(t *testing.T) {
	srv, _ := setupTestServer()

	// ct_1 opens cmp_alpha twice, and cmp_beta once
	// ct_2 opens cmp_alpha once
	payload := `[
		{"event_id":"evt_op1","campaign_id":"cmp_alpha","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T09:00:00Z"},
		{"event_id":"evt_op2","campaign_id":"cmp_alpha","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T09:10:00Z"},
		{"event_id":"evt_op3","campaign_id":"cmp_alpha","contact_id":"ct_2","type":"opened","timestamp":"2026-08-10T09:20:00Z"},
		{"event_id":"evt_op4","campaign_id":"cmp_beta","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T09:30:00Z"}
	]`

	req := httptest.NewRequest("POST", "/events", bytes.NewBufferString(payload))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	// Check cmp_alpha stats: opened=3, unique_opens=2
	reqAlpha := httptest.NewRequest("GET", "/campaigns/cmp_alpha/stats", nil)
	wAlpha := httptest.NewRecorder()
	srv.ServeHTTP(wAlpha, reqAlpha)

	var statsAlpha CampaignStats
	_ = json.Unmarshal(wAlpha.Body.Bytes(), &statsAlpha)
	if statsAlpha.Opened != 3 || statsAlpha.UniqueOpens != 2 {
		t.Errorf("cmp_alpha: expected opened=3, unique_opens=2; got opened=%d, unique_opens=%d",
			statsAlpha.Opened, statsAlpha.UniqueOpens)
	}

	// Check cmp_beta stats: opened=1, unique_opens=1 (ct_1 opening in cmp_alpha does NOT suppress cmp_beta)
	reqBeta := httptest.NewRequest("GET", "/campaigns/cmp_beta/stats", nil)
	wBeta := httptest.NewRecorder()
	srv.ServeHTTP(wBeta, reqBeta)

	var statsBeta CampaignStats
	_ = json.Unmarshal(wBeta.Body.Bytes(), &statsBeta)
	if statsBeta.Opened != 1 || statsBeta.UniqueOpens != 1 {
		t.Errorf("cmp_beta: expected opened=1, unique_opens=1; got opened=%d, unique_opens=%d",
			statsBeta.Opened, statsBeta.UniqueOpens)
	}
}

func TestPostEvents_PartialBatchFaultTolerance(t *testing.T) {
	srv, _ := setupTestServer()

	// 1 valid, 1 bad time, 1 missing event_id, 1 invalid type
	payload := `[
		{"event_id":"evt_good","campaign_id":"cmp_test","contact_id":"ct_1","type":"delivered","timestamp":"2026-08-10T10:00:00Z"},
		{"event_id":"evt_bad_time","campaign_id":"cmp_test","contact_id":"ct_1","type":"delivered","timestamp":"not-a-time"},
		{"event_id":"","campaign_id":"cmp_test","contact_id":"ct_1","type":"delivered","timestamp":"2026-08-10T10:00:00Z"},
		{"event_id":"evt_bad_type","campaign_id":"cmp_test","contact_id":"ct_1","type":"unknown_type","timestamp":"2026-08-10T10:00:00Z"}
	]`

	req := httptest.NewRequest("POST", "/events", bytes.NewBufferString(payload))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 for partial batch, got %d", w.Code)
	}

	var res BatchIngestResult
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res.Status != "partial_success" || res.Accepted != 1 || res.Rejected != 3 {
		t.Errorf("expected partial_success, 1 accepted, 3 rejected; got %+v", res)
	}
	if len(res.Errors) != 3 {
		t.Errorf("expected 3 error items, got %d", len(res.Errors))
	}
}

func TestGetStats_NotFound(t *testing.T) {
	srv, _ := setupTestServer()

	req := httptest.NewRequest("GET", "/campaigns/non_existent/stats", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent campaign, got %d", w.Code)
	}
}

func TestGetEvents_Pagination(t *testing.T) {
	srv, _ := setupTestServer()

	payload := `[
		{"event_id":"evt_p1","campaign_id":"cmp_p","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T09:00:00Z"},
		{"event_id":"evt_p2","campaign_id":"cmp_p","contact_id":"ct_1","type":"delivered","timestamp":"2026-08-10T09:01:00Z"},
		{"event_id":"evt_p3","campaign_id":"cmp_p","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T09:02:00Z"}
	]`
	req := httptest.NewRequest("POST", "/events", bytes.NewBufferString(payload))
	srv.ServeHTTP(httptest.NewRecorder(), req)

	// Query with limit=2, offset=1
	getReq := httptest.NewRequest("GET", "/campaigns/cmp_p/events?limit=2&offset=1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, getReq)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PaginatedEventsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Total != 3 || len(resp.Events) != 2 || resp.Events[0].EventID != "evt_p2" {
		t.Errorf("pagination failed: total=%d, count=%d, first_id=%s",
			resp.Total, len(resp.Events), resp.Events[0].EventID)
	}
}

func TestPostEvents_Concurrency(t *testing.T) {
	srv, _ := setupTestServer()

	var wg sync.WaitGroup
	workers := 10
	eventsPerWorker := 20

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			var batch []map[string]any
			for j := 0; j < eventsPerWorker; j++ {
				batch = append(batch, map[string]any{
					"event_id":    "evt_conc_" + strconv.Itoa(j), // intentional duplicate IDs across workers!
					"campaign_id": "cmp_conc",
					"contact_id":  "ct_" + strconv.Itoa(j),
					"type":        "delivered",
					"timestamp":   "2026-08-10T12:00:00Z",
				})
			}
			b, _ := json.Marshal(batch)
			req := httptest.NewRequest("POST", "/events", bytes.NewReader(b))
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
		}(i)
	}

	wg.Wait()

	// Since all 10 workers submitted the exact same 20 event IDs ("evt_conc_0" to "evt_conc_19"),
	// exactly 20 events should have been accepted, and the remaining 180 should be duplicates.
	statsReq := httptest.NewRequest("GET", "/campaigns/cmp_conc/stats", nil)
	wStats := httptest.NewRecorder()
	srv.ServeHTTP(wStats, statsReq)

	var stats CampaignStats
	_ = json.Unmarshal(wStats.Body.Bytes(), &stats)
	if stats.Delivered != 20 {
		t.Errorf("concurrency deduplication failed: expected delivered=20, got %d", stats.Delivered)
	}
}

func TestSeedDataFile_FullSurvival(t *testing.T) {
	srv, _ := setupTestServer()

	seedBytes, err := os.ReadFile("seed/events.json")
	if err != nil {
		t.Fatalf("could not read seed file: %v", err)
	}

	req := httptest.NewRequest("POST", "/events", bytes.NewReader(seedBytes))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from seed file ingest, got %d: %s", w.Code, w.Body.String())
	}

	var res BatchIngestResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	t.Logf("Seed ingestion results: received=%d, accepted=%d, duplicates=%d, rejected=%d",
		res.Received, res.Accepted, res.Duplicates, res.Rejected)

	if res.Received != 235 {
		t.Errorf("expected 235 received, got %d", res.Received)
	}
	if res.Accepted == 0 {
		t.Errorf("expected >0 accepted events, got 0")
	}
	if res.Rejected == 0 {
		t.Errorf("expected seed file to have rejected malformed records, got 0")
	}

	// Check that stats can be queried for cmp_summer_sale
	statsReq := httptest.NewRequest("GET", "/campaigns/cmp_summer_sale/stats", nil)
	wStats := httptest.NewRecorder()
	srv.ServeHTTP(wStats, statsReq)

	if wStats.Code != http.StatusOK {
		t.Errorf("expected 200 for cmp_summer_sale stats, got %d", wStats.Code)
	}
	var stats CampaignStats
	_ = json.Unmarshal(wStats.Body.Bytes(), &stats)
	t.Logf("cmp_summer_sale stats: %+v", stats)
}
