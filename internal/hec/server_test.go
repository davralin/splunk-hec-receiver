package hec

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func serve(server *Server, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func assertHECResponse(t *testing.T, response *httptest.ResponseRecorder, wantStatus, wantCode int, wantText string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("expected %d, got %d: %s", wantStatus, response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	var body struct {
		Code int    `json:"code"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != wantCode || body.Text != wantText {
		t.Fatalf("response = %#v, want code %d and text %q", body, wantCode, wantText)
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

func TestIngestValidation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		encoding   string
		wantStatus int
		wantCode   int
		wantText   string
	}{
		{"empty", "", "", http.StatusBadRequest, 5, "No data"},
		{"whitespace", " \r\n\t", "", http.StatusBadRequest, 5, "No data"},
		{"invalid json", "{", "", http.StatusBadRequest, 6, "Invalid data format"},
		{"unsupported encoding", `{"event":"test"}`, "br", http.StatusUnsupportedMediaType, 6, "Unsupported content encoding"},
		{"invalid gzip", "not gzip", "gzip", http.StatusBadRequest, 6, "Invalid gzip data"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(testConfig())
			request := httptest.NewRequest(http.MethodPost, "/services/collector/event", strings.NewReader(test.body))
			if test.encoding != "" {
				request.Header.Set("Content-Encoding", test.encoding)
			}
			assertHECResponse(t, serve(server, request), test.wantStatus, test.wantCode, test.wantText)
		})
	}
}

func TestIngestPreservesHeadersAndCRLFFraming(t *testing.T) {
	server := NewServer(testConfig())
	request := httptest.NewRequest(http.MethodPost, "/services/collector", strings.NewReader("\r\n {\"event\":1} \r {\"event\":2}\n"))
	request.Header.Set("__splunk_app_name", "collector")
	request.Header.Set("__splunk_app_version", "1.2.3")
	assertHECResponse(t, serve(server, request), http.StatusOK, 0, "Success")

	events := server.store.eventsAfter(0, 10)
	if len(events) != 2 || events[0].Endpoint != "/services/collector" || events[0].AppName != "collector" || events[0].AppVersion != "1.2.3" || string(events[0].Envelope) != `{"event":1}` {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestIngestRejectsRequestAndDecodedSizeLimits(t *testing.T) {
	config := testConfig()
	config.MaxRequestBytes = 10
	server := NewServer(config)
	request := httptest.NewRequest(http.MethodPost, "/services/collector/event", strings.NewReader(`{"event":"too-large"}`))
	assertHECResponse(t, serve(server, request), http.StatusRequestEntityTooLarge, 6, "Request body too large")

	config = testConfig()
	config.MaxDecodedBytes = 10
	server = NewServer(config)
	var payload bytes.Buffer
	writer := gzip.NewWriter(&payload)
	if _, err := writer.Write([]byte(`{"event":"too-large"}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/services/collector/event", &payload)
	request.Header.Set("Content-Encoding", "gzip")
	assertHECResponse(t, serve(server, request), http.StatusRequestEntityTooLarge, 6, "Decoded request body too large")
}

func TestIngestAcknowledgesOversizeEvents(t *testing.T) {
	config := testConfig()
	config.MaxEventBytes = 10
	server := NewServer(config)
	request := httptest.NewRequest(http.MethodPost, "/services/collector/event", strings.NewReader(`{"event":"too-large"}`))
	assertHECResponse(t, serve(server, request), http.StatusOK, 0, "Success")

	if events := server.store.eventsAfter(0, 10); len(events) != 0 {
		t.Fatalf("expected no retained events, got %#v", events)
	}
	stats := server.store.snapshot()
	if stats.AcceptedEvents != 1 || stats.OversizeEvents != 1 || stats.RetainedEvents != 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestTokenAllowlistRequiresExactSplunkPrefix(t *testing.T) {
	config := testConfig()
	config.AcceptedTokens = map[string]struct{}{"expected": {}}
	server := NewServer(config)
	for _, header := range []string{"", "Bearer expected", "splunk expected", "Splunk expected ", "Splunk wrong"} {
		t.Run(header, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/services/collector/event", strings.NewReader(`{"event":"denied"}`))
			request.Header.Set("Authorization", header)
			assertHECResponse(t, serve(server, request), http.StatusUnauthorized, 4, "Invalid token")
		})
	}
}

func TestIngestRejectsWhenAllRequestSlotsAreBusy(t *testing.T) {
	server := NewServer(testConfig())
	reader, writer := io.Pipe()
	firstRequest := httptest.NewRequest(http.MethodPost, "/services/collector/event", reader)
	finished := make(chan struct{})
	go func() {
		serve(server, firstRequest)
		close(finished)
	}()

	deadline := time.Now().Add(time.Second)
	for len(server.requests) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(server.requests) != 1 {
		t.Fatal("first request did not acquire the request slot")
	}
	secondRequest := httptest.NewRequest(http.MethodPost, "/services/collector/event", strings.NewReader(`{"event":"busy"}`))
	assertHECResponse(t, serve(server, secondRequest), http.StatusServiceUnavailable, 9, "Receiver is busy")
	_ = writer.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
}

func TestInspectionEndpoints(t *testing.T) {
	server := NewServer(testConfig())
	for _, event := range []string{`{"event":1}`, `{"event":2}`, `{"event":3}`} {
		assertHECResponse(t, serve(server, httptest.NewRequest(http.MethodPost, "/services/collector/event", strings.NewReader(event))), http.StatusOK, 0, "Success")
	}

	response := serve(server, httptest.NewRequest(http.MethodGet, "/api/v1/events?after=1&limit=1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var events struct {
		Events []CapturedEvent `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].ID != 2 {
		t.Fatalf("unexpected events: %#v", events.Events)
	}

	for _, path := range []string{"/api/v1/events?limit=0", "/api/v1/events?limit=1001", "/api/v1/events?after=-1"} {
		response = serve(server, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", path, response.Code)
		}
	}

	response = serve(server, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	var status struct {
		ACLEnabled bool  `json:"acl_enabled"`
		MaxEvents  int   `json:"max_events"`
		Stats      Stats `json:"stats"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.ACLEnabled || status.MaxEvents != 2 || status.Stats.AcceptedEvents != 3 || status.Stats.EvictedEvents != 1 || status.Stats.RetainedEvents != 2 {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestRoutesAndMethods(t *testing.T) {
	server := NewServer(testConfig())
	tests := []struct {
		method  string
		path    string
		want    int
		allow   string
		hecBody bool
	}{
		{http.MethodGet, "/services/collector", http.StatusMethodNotAllowed, http.MethodPost, true},
		{http.MethodPost, "/services/collector/health", http.StatusMethodNotAllowed, http.MethodGet, true},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed, http.MethodGet, false},
		{http.MethodPost, "/api/v1/events", http.StatusMethodNotAllowed, http.MethodGet, false},
		{http.MethodPost, "/api/v1/status", http.StatusMethodNotAllowed, http.MethodGet, false},
		{http.MethodGet, "/services/collector/health", http.StatusOK, "", true},
		{http.MethodGet, "/healthz", http.StatusOK, "", false},
		{http.MethodGet, "/missing", http.StatusNotFound, "", false},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := serve(server, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.want || response.Header().Get("Allow") != test.allow {
				t.Fatalf("status %d, allow %q; want status %d, allow %q", response.Code, response.Header().Get("Allow"), test.want, test.allow)
			}
			if test.hecBody {
				if test.want == http.StatusOK {
					assertHECResponse(t, response, test.want, 0, "Success")
				} else {
					assertHECResponse(t, response, test.want, 5, "Method not allowed")
				}
			}
		})
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
