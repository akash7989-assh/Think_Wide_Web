package main

import (
	"sync"
)

// Storage defines the persistence and retrieval interface for campaign events.
type Storage interface {
	IngestBatch(events []Event) (accepted int, duplicates int, err error)
	GetStats(campaignID string) (*CampaignStats, bool)
	GetEvents(campaignID string, limit, offset int) ([]Event, int, bool)
}

// MemoryStorage is a thread-safe, high-performance in-memory implementation of Storage.
type MemoryStorage struct {
	mu             sync.RWMutex
	seenEvents     map[string]bool             // event_id -> seen
	eventsByID     map[string]Event            // event_id -> Event
	campaignStats  map[string]*CampaignStats   // campaign_id -> stats
	uniqueOpeners  map[string]map[string]bool  // campaign_id -> contact_id -> opened
	campaignEvents map[string][]Event          // campaign_id -> list of raw events
}

// NewMemoryStorage instantiates an initialized MemoryStorage.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		seenEvents:     make(map[string]bool),
		eventsByID:     make(map[string]Event),
		campaignStats:  make(map[string]*CampaignStats),
		uniqueOpeners:  make(map[string]map[string]bool),
		campaignEvents: make(map[string][]Event),
	}
}

// IngestBatch atomically processes a slice of validated events.
// It enforces global deduplication by event_id, maintains campaign counters,
// tracks unique opens per contact per campaign, and stores raw event records.
func (s *MemoryStorage) IngestBatch(events []Event) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	accepted := 0
	duplicates := 0

	for _, ev := range events {
		// Rule 1: Providers retry; duplicate event_id must not affect statistics.
		if s.seenEvents[ev.EventID] {
			duplicates++
			continue
		}

		s.seenEvents[ev.EventID] = true
		s.eventsByID[ev.EventID] = ev
		s.campaignEvents[ev.CampaignID] = append(s.campaignEvents[ev.CampaignID], ev)

		// Ensure campaign stats record exists
		cs, ok := s.campaignStats[ev.CampaignID]
		if !ok {
			cs = &CampaignStats{CampaignID: ev.CampaignID}
			s.campaignStats[ev.CampaignID] = cs
		}

		// Ensure unique openers map exists for this campaign
		if _, exists := s.uniqueOpeners[ev.CampaignID]; !exists {
			s.uniqueOpeners[ev.CampaignID] = make(map[string]bool)
		}

		// Apply counter increments
		switch ev.Type {
		case TypeSent:
			cs.Sent++
		case TypeDelivered:
			cs.Delivered++
		case TypeOpened:
			cs.Opened++
			// Rule 2: unique_opens = distinct contacts that opened per campaign
			if !s.uniqueOpeners[ev.CampaignID][ev.ContactID] {
				s.uniqueOpeners[ev.CampaignID][ev.ContactID] = true
				cs.UniqueOpens++
			}
		case TypeClicked:
			cs.Clicked++
		}

		accepted++
	}

	return accepted, duplicates, nil
}

// GetStats returns a snapshot copy of the statistics for a given campaign.
func (s *MemoryStorage) GetStats(campaignID string) (*CampaignStats, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cs, exists := s.campaignStats[campaignID]
	if !exists {
		return nil, false
	}

	// Return a copy to prevent mutation outside the lock
	statsCopy := *cs
	return &statsCopy, true
}

// GetEvents returns a paginated list of events for a given campaign.
func (s *MemoryStorage) GetEvents(campaignID string, limit, offset int) ([]Event, int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	evList, exists := s.campaignEvents[campaignID]
	if !exists {
		return nil, 0, false
	}

	total := len(evList)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []Event{}, total, true
	}

	end := offset + limit
	if end > total {
		end = total
	}

	result := make([]Event, end-offset)
	copy(result, evList[offset:end])

	return result, total, true
}

