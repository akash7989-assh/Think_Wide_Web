package main

import (
	"fmt"
	"strings"
	"time"
)

// EventType constants
const (
	TypeSent      = "sent"
	TypeDelivered = "delivered"
	TypeOpened    = "opened"
	TypeClicked   = "clicked"
)

// Event represents a validated, normalized webhook event.
type Event struct {
	EventID    string         `json:"event_id"`
	CampaignID string         `json:"campaign_id"`
	ContactID  string         `json:"contact_id"`
	Type       string         `json:"type"` // sent | delivered | opened | clicked
	Timestamp  time.Time      `json:"timestamp"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// RawEvent is used for preliminary decoding before strict validation.
type RawEvent struct {
	EventID    string         `json:"event_id"`
	CampaignID string         `json:"campaign_id"`
	ContactID  string         `json:"contact_id"`
	Type       string         `json:"type"`
	Timestamp  string         `json:"timestamp"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// ValidateAndNormalize validates required fields, parses the timestamp,
// and normalizes the event type.
func (r *RawEvent) ValidateAndNormalize() (Event, error) {
	eventID := strings.TrimSpace(r.EventID)
	if eventID == "" {
		return Event{}, fmt.Errorf("missing or empty event_id")
	}

	campaignID := strings.TrimSpace(r.CampaignID)
	if campaignID == "" {
		return Event{}, fmt.Errorf("missing or empty campaign_id")
	}

	contactID := strings.TrimSpace(r.ContactID)
	if contactID == "" {
		return Event{}, fmt.Errorf("missing or empty contact_id")
	}

	evType := strings.ToLower(strings.TrimSpace(r.Type))
	switch evType {
	case TypeSent, TypeDelivered, TypeOpened, TypeClicked:
		// valid
	default:
		return Event{}, fmt.Errorf("invalid event type '%s'", r.Type)
	}

	rawTime := strings.TrimSpace(r.Timestamp)
	if rawTime == "" {
		return Event{}, fmt.Errorf("missing or empty timestamp")
	}

	// Supported time formats: standard RFC3339 / ISO8601
	parsedTime, err := parseTimestamp(rawTime)
	if err != nil {
		return Event{}, fmt.Errorf("invalid timestamp format: %w", err)
	}

	return Event{
		EventID:    eventID,
		CampaignID: campaignID,
		ContactID:  contactID,
		Type:       evType,
		Timestamp:  parsedTime.UTC(),
		Metadata:   r.Metadata,
	}, nil
}

func parseTimestamp(val string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
	}
	for _, layout := range formats {
		if t, err := time.Parse(layout, val); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse '%s' as RFC3339 timestamp", val)
}

// CampaignStats holds aggregated metrics for a campaign.
type CampaignStats struct {
	CampaignID  string `json:"campaign_id"`
	Sent        int    `json:"sent"`
	Delivered   int    `json:"delivered"`
	Opened      int    `json:"opened"`
	Clicked     int    `json:"clicked"`
	UniqueOpens int    `json:"unique_opens"`
}

// ItemError describes an error encountered for an individual event in a batch.
type ItemError struct {
	Index   int    `json:"index"`
	EventID string `json:"event_id,omitempty"`
	Reason  string `json:"reason"`
}

// BatchIngestResult is the response payload for POST /events.
type BatchIngestResult struct {
	Status     string      `json:"status"` // "success" | "partial_success" | "duplicate"
	Received   int         `json:"received"`
	Accepted   int         `json:"accepted"`
	Duplicates int         `json:"duplicates"`
	Rejected   int         `json:"rejected"`
	Errors     []ItemError `json:"errors,omitempty"`
}

// PaginatedEventsResponse represents paginated event history for GET /campaigns/{id}/events.
type PaginatedEventsResponse struct {
	CampaignID string  `json:"campaign_id"`
	Total      int     `json:"total"`
	Limit      int     `json:"limit"`
	Offset     int     `json:"offset"`
	Events     []Event `json:"events"`
}

