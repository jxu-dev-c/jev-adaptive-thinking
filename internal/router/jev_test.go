package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJevDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason, choice string
		status                     int
	}{
		{"simple", `{"id":"req-1","answers":{"route":{"type":"choice","choice":"simple","probabilities":{"simple":0.9,"standard":0.1}}}}`, "", "simple", 200},
		{"standard", `{"answers":{"route":{"type":"choice","choice":"standard"}}}`, "", "standard", 200},
		{"complex", `{"answers":{"route":{"type":"choice","choice":"complex"}}}`, "", "complex", 200},
		{"http", `{"error":"private prompt and API key"}`, "jev_http_error", "", 429},
		{"json", `{`, "jev_invalid_response", "", 200},
		{"wrong type", `{"answers":{"route":{"type":"score","choice":"simple"}}}`, "jev_invalid_answer", "", 200},
		{"unknown", `{"answers":{"route":{"type":"choice","choice":"attacker-model"}}}`, "jev_unknown_choice", "", 200},
		{"invalid probability", `{"answers":{"route":{"type":"choice","choice":"simple","probabilities":{"simple":2}}}}`, "jev_invalid_probabilities", "", 200},
		{"unknown probability", `{"answers":{"route":{"type":"choice","choice":"simple","probabilities":{"private-data":0.2}}}}`, "jev_invalid_probabilities", "", 200},
		{"missing answer", `{}`, "jev_invalid_answer", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("incorrect auth/method")
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["state"] != "first prompt" || payload["model"] != "typesafe/jev-1.13" {
					t.Error("incorrect payload")
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer s.Close()
			j := NewJevClient()
			j.endpoint = s.URL
			j.apiKey = func() string { return "test-key" }
			d := j.jevDecise(context.Background(), testConfig(), "first prompt")
			if d.Reason != tc.reason || d.Choice != tc.choice || calls.Load() != 1 {
				t.Fatalf("got %+v calls=%d", d, calls.Load())
			}
		})
	}
}

func TestJevTimeoutNoRetryAndMissingKey(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
	}))
	defer s.Close()
	defer close(release)
	j := NewJevClient()
	j.endpoint = s.URL
	j.apiKey = func() string { return "key" }
	cfg := testConfig()
	cfg.TimeoutMS = 30
	if d := j.Decide(context.Background(), cfg, "prompt"); d.Reason != "jev_timeout" {
		t.Fatalf("got %+v", d)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected retry")
	}
	j.apiKey = func() string { return "" }
	if d := j.Decide(context.Background(), cfg, "prompt"); d.Reason != "missing_api_key" {
		t.Fatal(d)
	}
	if calls.Load() != 1 {
		t.Fatal("request sent without key")
	}
}

func TestJevDoesNotFollowRedirectOrAcceptHugeResponse(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	j := NewJevClient()
	j.endpoint = s.URL
	j.apiKey = func() string { return "secret" }
	if d := j.Decide(context.Background(), testConfig(), "private"); d.Reason != "jev_http_error" || leaked.Load() {
		t.Fatal(d)
	}
	s.Close()
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", (1<<20)+1)) }))
	defer s.Close()
	j.endpoint = s.URL
	if d := j.Decide(context.Background(), testConfig(), "prompt"); d.Reason != "jev_invalid_response" {
		t.Fatal(d)
	}
}

func TestLiveJev(t *testing.T) {
	if os.Getenv("JEV_LIVE_TEST") != "1" {
		t.Skip("set JEV_LIVE_TEST=1 to make three billable OpenRouter calls")
	}
	if os.Getenv("OPEN_ROUTER_API_KEY") == "" {
		t.Fatal("OPEN_ROUTER_API_KEY is required")
	}
	j := NewJevClient()
	for _, sample := range []struct{ tier, prompt string }{
		{"simple", "Extract the email address from this text: Contact Jane at jane@example.com."},
		{"standard", "Implement a paginated REST endpoint for our existing CRUD application and test input validation."},
		{"complex", "Design and justify a multi-region distributed transaction protocol with a formal safety argument under network partitions and investigate a subtle write-skew anomaly."},
	} {
		var observed Decision
		calls := 0
		router, err := New(testConfig(), nil, func(ctx context.Context, cfg Config, prompt string) Decision {
			calls++
			observed = j.Decide(ctx, cfg, prompt)
			return observed
		})
		if err != nil {
			t.Fatal(err)
		}
		req := testRequest("live-" + sample.tier)
		req.Body, _ = json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": sample.prompt}}})
		start := time.Now()
		first := router.Route(req)
		firstElapsed := time.Since(start)
		start = time.Now()
		cached := router.Route(req)
		t.Logf("expected=%s selected=%s model=%s first=%s cache=%s reason=%s", sample.tier, observed.Choice, first.TargetModel, firstElapsed, time.Since(start), observed.Reason)
		if observed.Reason != "" {
			t.Errorf("Jev failed: %s", observed.Reason)
		}
		if first.TargetModel != cached.TargetModel || calls != 1 {
			t.Error("cache did not preserve the first decision")
		}
		// Classification is an empirical observation, not a deterministic unit assertion.
	}
}
