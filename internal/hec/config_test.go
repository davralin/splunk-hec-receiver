package hec

import "testing"

func TestParseBytes(t *testing.T) {
	tests := map[string]int64{
		"1024": 1024,
		"1KiB": 1024,
		"2MiB": 2 << 20,
		"3GB":  3 << 30,
	}
	for input, expected := range tests {
		actual, err := parseBytes(input)
		if err != nil {
			t.Fatalf("parseBytes(%q): %v", input, err)
		}
		if actual != expected {
			t.Fatalf("parseBytes(%q) = %d, want %d", input, actual, expected)
		}
	}
}

func TestParseBytesRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{"", "1XB", "999999999999999999999GiB"} {
		if _, err := parseBytes(input); err == nil {
			t.Errorf("parseBytes(%q) succeeded", input)
		}
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.ListenAddress != defaultListenAddress || config.MaxRequestBytes != defaultMaxRequestBytes || config.MaxDecodedBytes != defaultMaxDecodedBytes || config.MaxCaptureBytes != defaultMaxCaptureBytes || config.MaxEventBytes != defaultMaxEventBytes || config.MaxEvents != defaultMaxEvents || config.MaxConcurrentRequests != defaultMaxConcurrent {
		t.Fatalf("unexpected defaults: %#v", config)
	}
	if len(config.AcceptedTokens) != 0 {
		t.Fatalf("expected no configured tokens, got %#v", config.AcceptedTokens)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("HEC_LISTEN_ADDRESS", "127.0.0.1:9000")
	t.Setenv("HEC_ACCEPTED_TOKENS", " first,second ")
	t.Setenv("HEC_MAX_REQUEST_BYTES", "2KiB")
	t.Setenv("HEC_MAX_DECOMPRESSED_BYTES", "3KiB")
	t.Setenv("HEC_MAX_CAPTURE_BYTES", "4KiB")
	t.Setenv("HEC_MAX_EVENT_BYTES", "1KiB")
	t.Setenv("HEC_MAX_EVENTS", "7")
	t.Setenv("HEC_MAX_CONCURRENT_REQUESTS", "2")

	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.ListenAddress != "127.0.0.1:9000" || config.MaxRequestBytes != 2<<10 || config.MaxDecodedBytes != 3<<10 || config.MaxCaptureBytes != 4<<10 || config.MaxEventBytes != 1<<10 || config.MaxEvents != 7 || config.MaxConcurrentRequests != 2 {
		t.Fatalf("unexpected configuration: %#v", config)
	}
	if _, ok := config.AcceptedTokens["first"]; !ok {
		t.Fatalf("first token missing: %#v", config.AcceptedTokens)
	}
	if _, ok := config.AcceptedTokens["second"]; !ok {
		t.Fatalf("second token missing: %#v", config.AcceptedTokens)
	}
}

func TestConfigFromEnvRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		set  func(*testing.T)
		want string
	}{
		{"invalid byte size", func(t *testing.T) { t.Setenv("HEC_MAX_REQUEST_BYTES", "zero") }, "HEC_MAX_REQUEST_BYTES must be a positive byte size"},
		{"negative byte size", func(t *testing.T) { t.Setenv("HEC_MAX_REQUEST_BYTES", "-1") }, "HEC_MAX_REQUEST_BYTES must be a positive byte size"},
		{"zero event count", func(t *testing.T) { t.Setenv("HEC_MAX_EVENTS", "0") }, "HEC_MAX_EVENTS must be a positive integer"},
		{"event limit exceeds capture", func(t *testing.T) { t.Setenv("HEC_MAX_CAPTURE_BYTES", "1KiB"); t.Setenv("HEC_MAX_EVENT_BYTES", "2KiB") }, "HEC_MAX_EVENT_BYTES cannot exceed HEC_MAX_CAPTURE_BYTES"},
		{"empty token", func(t *testing.T) { t.Setenv("HEC_ACCEPTED_TOKENS", "first, ") }, "HEC_ACCEPTED_TOKENS contains an empty token"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.set(t)
			_, err := ConfigFromEnv()
			if err == nil || err.Error() != test.want {
				t.Fatalf("ConfigFromEnv() error = %v, want %q", err, test.want)
			}
		})
	}
}
