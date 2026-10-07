package hec

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testConfig() Config {
	return Config{
		ListenAddress:         ":8088",
		MaxRequestBytes:       1024,
		MaxDecodedBytes:       1024,
		MaxCaptureBytes:       512,
		MaxEventBytes:         256,
		MaxEvents:             2,
		MaxConcurrentRequests: 1,
	}
}

func TestIngestsNDJSONAndExposesEvents(t *testing.T) {
	server := NewServer(testConfig())
	request := httptest.NewRequest(http.MethodPost, "/services/collector/event", bytes.NewBufferString(`{"event":"first"}`+"\n"+`{"event":"second"}`))
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	var body struct {
		Events []CapturedEvent `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 2 || string(body.Events[1].Envelope) != `{"event":"second"}` {
		t.Fatalf("unexpected events: %#v", body.Events)
	}
}

func TestAcceptsGzip(t *testing.T) {
	server := NewServer(testConfig())
	var payload bytes.Buffer
	writer := gzip.NewWriter(&payload)
	if _, err := writer.Write([]byte(`{"event":"compressed"}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/services/collector", &payload)
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
}

func TestTokenAllowlist(t *testing.T) {
	config := testConfig()
	config.AcceptedTokens = map[string]struct{}{"expected": {}}
	server := NewServer(config)

	request := httptest.NewRequest(http.MethodPost, "/services/collector/event", bytes.NewBufferString(`{"event":"denied"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/services/collector/event", bytes.NewBufferString(`{"event":"accepted"}`))
	request.Header.Set("Authorization", "Splunk expected")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestRejectsOversizeDecodedPayload(t *testing.T) {
	config := testConfig()
	config.MaxDecodedBytes = 10
	server := NewServer(config)
	request := httptest.NewRequest(http.MethodPost, "/services/collector/event", bytes.NewBufferString(`{"event":"too-large"}`))
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", response.Code)
	}
}

func TestEvictsOldestEvents(t *testing.T) {
	server := NewServer(testConfig())
	for _, event := range []string{`{"event":1}`, `{"event":2}`, `{"event":3}`} {
		request := httptest.NewRequest(http.MethodPost, "/services/collector/event", bytes.NewBufferString(event))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", response.Code)
		}
	}

	events := server.store.eventsAfter(0, 10)
	if len(events) != 2 || string(events[0].Envelope) != `{"event":2}` {
		t.Fatalf("expected two newest events, got %#v", events)
	}
	if stats := server.store.snapshot(); stats.EvictedEvents != 1 {
		t.Fatalf("expected one eviction, got %d", stats.EvictedEvents)
	}
}
