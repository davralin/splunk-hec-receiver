package hec

import (
	"encoding/json"
	"sync"
	"time"
)

type CapturedEvent struct {
	ID         uint64          `json:"id"`
	Received   time.Time       `json:"received"`
	Endpoint   string          `json:"endpoint"`
	AppName    string          `json:"app_name,omitempty"`
	AppVersion string          `json:"app_version,omitempty"`
	Envelope   json.RawMessage `json:"envelope"`
}

type Stats struct {
	AcceptedEvents uint64 `json:"accepted_events"`
	RetainedEvents uint64 `json:"retained_events"`
	RetainedBytes  int64  `json:"retained_bytes"`
	EvictedEvents  uint64 `json:"evicted_events"`
	OversizeEvents uint64 `json:"oversize_events"`
}

type store struct {
	mu        sync.RWMutex
	events    []CapturedEvent
	bytes     int64
	nextID    uint64
	maxBytes  int64
	maxEvents int
	stats     Stats
}

func newStore(maxBytes int64, maxEvents int) *store {
	return &store{maxBytes: maxBytes, maxEvents: maxEvents}
}

func (s *store) add(endpoint, appName, appVersion string, envelope []byte, maxEventBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stats.AcceptedEvents++
	if int64(len(envelope)) > maxEventBytes || int64(len(envelope)) > s.maxBytes {
		s.stats.OversizeEvents++
		return
	}

	copyEnvelope := append(json.RawMessage(nil), envelope...)
	s.nextID++
	event := CapturedEvent{
		ID:         s.nextID,
		Received:   time.Now().UTC(),
		Endpoint:   endpoint,
		AppName:    appName,
		AppVersion: appVersion,
		Envelope:   copyEnvelope,
	}
	s.events = append(s.events, event)
	s.bytes += int64(len(copyEnvelope))

	for len(s.events) > s.maxEvents || s.bytes > s.maxBytes {
		s.bytes -= int64(len(s.events[0].Envelope))
		s.events = s.events[1:]
		s.stats.EvictedEvents++
	}
	s.stats.RetainedEvents = uint64(len(s.events))
	s.stats.RetainedBytes = s.bytes
}

func (s *store) eventsAfter(after uint64, limit int) []CapturedEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]CapturedEvent, 0, limit)
	for _, event := range s.events {
		if event.ID <= after {
			continue
		}
		result = append(result, event)
		if len(result) == limit {
			break
		}
	}
	return result
}

func (s *store) snapshot() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}
