package hec

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultListenAddress         = ":8088"
	defaultMaxRequestBytes int64 = 4 << 20
	defaultMaxDecodedBytes int64 = 16 << 20
	defaultMaxCaptureBytes int64 = 64 << 20
	defaultMaxEventBytes   int64 = 1 << 20
	defaultMaxEvents             = 10_000
	defaultMaxConcurrent         = 4
)

type Config struct {
	ListenAddress         string
	AcceptedTokens        map[string]struct{}
	MaxRequestBytes       int64
	MaxDecodedBytes       int64
	MaxCaptureBytes       int64
	MaxEventBytes         int64
	MaxEvents             int
	MaxConcurrentRequests int
}

func ConfigFromEnv() (Config, error) {
	config := Config{
		ListenAddress:         valueOrDefault("HEC_LISTEN_ADDRESS", defaultListenAddress),
		MaxRequestBytes:       defaultMaxRequestBytes,
		MaxDecodedBytes:       defaultMaxDecodedBytes,
		MaxCaptureBytes:       defaultMaxCaptureBytes,
		MaxEventBytes:         defaultMaxEventBytes,
		MaxEvents:             defaultMaxEvents,
		MaxConcurrentRequests: defaultMaxConcurrent,
	}

	var err error
	if config.MaxRequestBytes, err = bytesFromEnv("HEC_MAX_REQUEST_BYTES", config.MaxRequestBytes); err != nil {
		return Config{}, err
	}
	if config.MaxDecodedBytes, err = bytesFromEnv("HEC_MAX_DECOMPRESSED_BYTES", config.MaxDecodedBytes); err != nil {
		return Config{}, err
	}
	if config.MaxCaptureBytes, err = bytesFromEnv("HEC_MAX_CAPTURE_BYTES", config.MaxCaptureBytes); err != nil {
		return Config{}, err
	}
	if config.MaxEventBytes, err = bytesFromEnv("HEC_MAX_EVENT_BYTES", config.MaxEventBytes); err != nil {
		return Config{}, err
	}
	if config.MaxEvents, err = intFromEnv("HEC_MAX_EVENTS", config.MaxEvents); err != nil {
		return Config{}, err
	}
	if config.MaxConcurrentRequests, err = intFromEnv("HEC_MAX_CONCURRENT_REQUESTS", config.MaxConcurrentRequests); err != nil {
		return Config{}, err
	}
	if config.MaxEventBytes > config.MaxCaptureBytes {
		return Config{}, fmt.Errorf("HEC_MAX_EVENT_BYTES cannot exceed HEC_MAX_CAPTURE_BYTES")
	}

	if tokens := os.Getenv("HEC_ACCEPTED_TOKENS"); tokens != "" {
		config.AcceptedTokens = make(map[string]struct{})
		for _, token := range strings.Split(tokens, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				return Config{}, fmt.Errorf("HEC_ACCEPTED_TOKENS contains an empty token")
			}
			config.AcceptedTokens[token] = struct{}{}
		}
	}

	return config, nil
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func bytesFromEnv(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := parseBytes(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive byte size", name)
	}
	return parsed, nil
}

func intFromEnv(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func parseBytes(value string) (int64, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		factor int64
	}{
		{"KIB", 1 << 10},
		{"MIB", 1 << 20},
		{"GIB", 1 << 30},
		{"KB", 1 << 10},
		{"MB", 1 << 20},
		{"GB", 1 << 30},
		{"B", 1},
	} {
		suffix, factor := unit.suffix, unit.factor
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
			multiplier = factor
			break
		}
	}
	amount, err := strconv.ParseInt(value, 10, 64)
	if err != nil || amount > (1<<63-1)/multiplier {
		return 0, fmt.Errorf("invalid byte size")
	}
	return amount * multiplier, nil
}
