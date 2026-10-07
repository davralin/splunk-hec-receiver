package hec

import (
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Server struct {
	config   Config
	store    *store
	requests chan struct{}
}

func NewServer(config Config) *Server {
	return &Server{
		config:   config,
		store:    newStore(config.MaxCaptureBytes, config.MaxEvents),
		requests: make(chan struct{}, config.MaxConcurrentRequests),
	}
}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/services/collector", "/services/collector/event":
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			writeHECError(response, http.StatusMethodNotAllowed, "Method not allowed", 5)
			return
		}
		s.ingest(response, request)
	case "/services/collector/health":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			writeHECError(response, http.StatusMethodNotAllowed, "Method not allowed", 5)
			return
		}
		writeHECSuccess(response)
	case "/healthz":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusOK)
	case "/api/v1/events":
		s.events(response, request)
	case "/api/v1/status":
		s.status(response, request)
	default:
		http.NotFound(response, request)
	}
}

func (s *Server) ingest(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request.Header.Get("Authorization")) {
		writeHECError(response, http.StatusUnauthorized, "Invalid token", 4)
		return
	}

	select {
	case s.requests <- struct{}{}:
		defer func() { <-s.requests }()
	default:
		writeHECError(response, http.StatusServiceUnavailable, "Receiver is busy", 9)
		return
	}

	body, err := s.readBody(response, request)
	if err != nil {
		return
	}
	if len(body) == 0 {
		writeHECError(response, http.StatusBadRequest, "No data", 5)
		return
	}

	lines := splitLines(body)
	if len(lines) == 0 {
		writeHECError(response, http.StatusBadRequest, "No data", 5)
		return
	}
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			writeHECError(response, http.StatusBadRequest, "Invalid data format", 6)
			return
		}
		s.store.add(request.URL.Path, request.Header.Get("__splunk_app_name"), request.Header.Get("__splunk_app_version"), line, s.config.MaxEventBytes)
	}
	writeHECSuccess(response)
}

func (s *Server) readBody(response http.ResponseWriter, request *http.Request) ([]byte, error) {
	if request.ContentLength > s.config.MaxRequestBytes {
		writeHECError(response, http.StatusRequestEntityTooLarge, "Request body too large", 6)
		return nil, errors.New("request body too large")
	}
	limitedBody := http.MaxBytesReader(response, request.Body, s.config.MaxRequestBytes)
	defer limitedBody.Close()

	var reader io.Reader = limitedBody
	if encoding := request.Header.Get("Content-Encoding"); encoding != "" {
		if !strings.EqualFold(encoding, "gzip") {
			writeHECError(response, http.StatusUnsupportedMediaType, "Unsupported content encoding", 6)
			return nil, errors.New("unsupported content encoding")
		}
		gzipReader, err := gzip.NewReader(limitedBody)
		if err != nil {
			writeHECError(response, http.StatusBadRequest, "Invalid gzip data", 6)
			return nil, err
		}
		defer gzipReader.Close()
		reader = gzipReader
	}

	body, err := io.ReadAll(io.LimitReader(reader, s.config.MaxDecodedBytes+1))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeHECError(response, http.StatusRequestEntityTooLarge, "Request body too large", 6)
		} else {
			writeHECError(response, http.StatusBadRequest, "Unable to read request body", 6)
		}
		return nil, err
	}
	if int64(len(body)) > s.config.MaxDecodedBytes {
		writeHECError(response, http.StatusRequestEntityTooLarge, "Decoded request body too large", 6)
		return nil, errors.New("decoded request body too large")
	}
	return body, nil
}

func (s *Server) authorized(header string) bool {
	if len(s.config.AcceptedTokens) == 0 {
		return true
	}
	const prefix = "Splunk "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	token := strings.TrimPrefix(header, prefix)
	for accepted := range s.config.AcceptedTokens {
		if subtle.ConstantTimeCompare([]byte(token), []byte(accepted)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) events(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 100
	if value := request.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 1000 {
			http.Error(response, "limit must be between 1 and 1000", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	var after uint64
	if value := request.URL.Query().Get("after"); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			http.Error(response, "after must be an unsigned integer", http.StatusBadRequest)
			return
		}
		after = parsed
	}
	writeJSON(response, http.StatusOK, map[string]any{"events": s.store.eventsAfter(after, limit)})
}

func (s *Server) status(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"acl_enabled":       len(s.config.AcceptedTokens) > 0,
		"max_capture_bytes": s.config.MaxCaptureBytes,
		"max_events":        s.config.MaxEvents,
		"stats":             s.store.snapshot(),
	})
}

func splitLines(body []byte) [][]byte {
	lines := bytes.FieldsFunc(body, func(r rune) bool { return r == '\n' || r == '\r' })
	result := make([][]byte, 0, len(lines))
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) != 0 {
			result = append(result, trimmed)
		}
	}
	return result
}

func writeHECSuccess(response http.ResponseWriter) {
	writeJSON(response, http.StatusOK, map[string]any{"text": "Success", "code": 0})
}

func writeHECError(response http.ResponseWriter, status int, text string, code int) {
	writeJSON(response, status, map[string]any{"text": text, "code": code})
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}
