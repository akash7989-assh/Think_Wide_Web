package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// Server encapsulates the HTTP router and storage dependency.
type Server struct {
	store Storage
	mux   *http.ServeMux
}

// NewServer initializes routes and attaches the storage engine.
func NewServer(store Storage) *Server {
	s := &Server{
		store: store,
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /events", s.handlePostEvents)
	s.mux.HandleFunc("GET /campaigns/{campaignID}/stats", s.handleGetStats)
	s.mux.HandleFunc("GET /campaigns/{campaignID}/events", s.handleGetEvents)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// handlePostEvents ingests a JSON array of events from providers.
// Resilient decoding: uses []json.RawMessage so that single malformed items
// do not cause the entire batch of 200+ events to be rejected.
func (s *Server) handlePostEvents(w http.ResponseWriter, r *http.Request) {
	// Guard against excessively large request bodies (10 MB limit)
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read request body: " + err.Error()})
		return
	}

	trimmed := strings.TrimSpace(string(bodyBytes))
	if trimmed == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body cannot be empty"})
		return
	}

	var rawItems []json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &rawItems); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "request body must be a valid JSON array of event objects",
		})
		return
	}

	validEvents := make([]Event, 0, len(rawItems))
	itemErrors := make([]ItemError, 0)

	for i, rawItem := range rawItems {
		var raw RawEvent
		if err := json.Unmarshal(rawItem, &raw); err != nil {
			itemErrors = append(itemErrors, ItemError{
				Index:  i,
				Reason: "malformed JSON object: " + err.Error(),
			})
			continue
		}

		ev, err := raw.ValidateAndNormalize()
		if err != nil {
			itemErrors = append(itemErrors, ItemError{
				Index:   i,
				EventID: raw.EventID,
				Reason:  err.Error(),
			})
			continue
		}

		validEvents = append(validEvents, ev)
	}

	// Persist valid events through the storage layer
	accepted, duplicates, err := s.store.IngestBatch(validEvents)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal storage error: " + err.Error()})
		return
	}

	rejected := len(itemErrors)
	statusStr := "success"
	httpStatus := http.StatusOK

	if rejected > 0 && accepted == 0 && duplicates == 0 {
		statusStr = "failed"
		httpStatus = http.StatusBadRequest
	} else if rejected > 0 {
		statusStr = "partial_success"
		httpStatus = http.StatusOK // 200 with diagnostics to prevent infinite provider retry loops
	}

	result := BatchIngestResult{
		Status:     statusStr,
		Received:   len(rawItems),
		Accepted:   accepted,
		Duplicates: duplicates,
		Rejected:   rejected,
		Errors:     itemErrors,
	}

	writeJSON(w, httpStatus, result)
}

// handleGetStats returns aggregated statistics for a single campaign.
func (s *Server) handleGetStats(w http.ResponseWriter, r *http.Request) {
	campaignID := strings.TrimSpace(r.PathValue("campaignID"))
	if campaignID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "campaignID is required"})
		return
	}

	stats, found := s.store.GetStats(campaignID)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":       "campaign not found",
			"campaign_id": campaignID,
		})
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// handleGetEvents returns a paginated list of ingested events for a campaign.
func (s *Server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	campaignID := strings.TrimSpace(r.PathValue("campaignID"))
	if campaignID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "campaignID is required"})
		return
	}

	limit := 50
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = l
			if limit > 100 {
				limit = 100
			}
		}
	}

	offset := 0
	if oStr := r.URL.Query().Get("offset"); oStr != "" {
		if o, err := strconv.Atoi(oStr); err == nil && o >= 0 {
			offset = o
		}
	}

	events, total, found := s.store.GetEvents(campaignID, limit, offset)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":       "campaign not found",
			"campaign_id": campaignID,
		})
		return
	}

	resp := PaginatedEventsResponse{
		CampaignID: campaignID,
		Total:      total,
		Limit:      limit,
		Offset:     offset,
		Events:     events,
	}

	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}

