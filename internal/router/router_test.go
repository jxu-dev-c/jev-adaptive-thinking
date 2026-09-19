package router

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func testConfig() Config {
	return Config{JevModel: "typesafe/jev-1.13", TimeoutMS: 1500, Fallback: "standard", Candidates: []Candidate{
		{Key: "simple", Provider: "deepseek", Model: "deepseek-v4-flash:deepseek", Description: "Lowest cost: simple questions, extraction, rewriting, and small clearly specified code changes."},
		{Key: "standard", Provider: "codex", Model: "gpt-5.6-sol", Description: "Balanced: routine software development, ordinary debugging, and moderately complex multi-step tasks. Default for ambiguous ordinary tasks."},
		{Key: "complex", Provider: "codex", Model: "gpt-5.6-astra", Description: "Highest capability: difficult reasoning, architectural design, deep debugging, and highly complex tasks."},
	}}
}

func TestDecisionPanicReleasesSessionWaiters(t *testing.T) {
	var calls atomic.Int32
	r, _ := New(testConfig(), nil, func(context.Context, Config, string) Decision {
		calls.Add(1)
		panic("sensitive dependency failure")
	})
	done := make(chan pluginapi.ModelRouteResponse, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- r.Route(testRequest("same")) }()
	}
	for i := 0; i < 2; i++ {
		select {
		case result := <-done:
			if result.TargetModel != "gpt-5.6-sol" || result.Reason != "jev_fallback_decision_panic" {
				t.Fatalf("unexpected result: %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("session waiter stuck after decision panic")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("panic fallback was not locked")
	}
}

func testRequest(id string) pluginapi.ModelRouteRequest {
	return pluginapi.ModelRouteRequest{
		RequestedModel: AutoModel, SourceFormat: "openai",
		Headers:  http.Header{"Session-Id": []string{id}},
		Body:     []byte(`{"messages":[{"role":"user","content":"Write a function"}]}`),
		Metadata: map[string]any{"caller_scope": "caller-a"},
	}
}

func TestFirstPrompt(t *testing.T) {
	cases := []struct{ name, format, body, text, reason string }{
		{"chat", "openai", `{"messages":[{"role":"system","content":"ignored"},{"role":"developer","content":"ignored"},{"role":"user","content":"first"},{"role":"user","content":"later"}]}`, "first", ""},
		{"response string", "openai-response", `{"input":"hello"}`, "hello", ""},
		{"response array", "openai-response", `{"input":[{"type":"function_call_output","output":"ignored"},{"role":"user","content":[{"type":"input_text","text":"first"}]}]}`, "first", ""},
		{"claude", "claude", `{"messages":[{"role":"user","content":[{"type":"text","text":"first"},{"type":"tool_result","content":"ignored"},{"type":"text","text":"second"}]}]}`, "first\nsecond", ""},
		{"skip tools", "claude", `{"messages":[{"role":"user","content":[{"type":"tool_result","content":"ignored"}]},{"role":"user","content":"task"}]}`, "task", ""},
		{"image first", "openai", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"secret"}}]},{"role":"user","content":"later"}]}`, "", "multimodal_prompt"},
		{"mixed image", "claude", `{"messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image","source":{}}]}]}`, "", "multimodal_prompt"},
		{"empty first", "openai", `{"messages":[{"role":"user","content":""},{"role":"user","content":"later"}]}`, "", "missing_prompt"},
		{"no user", "openai", `{"messages":[{"role":"assistant","content":"ignored"}]}`, "", "missing_prompt"},
		{"invalid", "openai", `{`, "", "invalid_body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, reason := firstPrompt([]byte(tc.body), tc.format)
			if text != tc.text || reason != tc.reason {
				t.Fatalf("got %q/%q, want %q/%q", text, reason, tc.text, tc.reason)
			}
		})
	}
	text, _ := boundedText(strings.Repeat("中", 9000))
	if len([]rune(text)) != 8000 {
		t.Fatal("Unicode limit not applied")
	}
}

func TestSessionIdentity(t *testing.T) {
	a := testRequest("session-a")
	b := testRequest("session-a")
	b.Body = []byte(`{"messages":[{"role":"user","content":"compacted history"}]}`)
	if sessionKey(a) != sessionKey(b) {
		t.Fatal("body changes altered explicit identity")
	}
	b.Metadata["caller_scope"] = "caller-b"
	if sessionKey(a) == sessionKey(b) {
		t.Fatal("callers were merged")
	}
	b = testRequest("session-a")
	b.Headers.Set("X-Claude-Code-Agent-Id", "child")
	if sessionKey(a) == sessionKey(b) {
		t.Fatal("subagent was merged with parent")
	}
	for _, h := range []string{"X-Request-Id", "X-Client-Request-Id", "X-Session-Affinity"} {
		r := testRequest("")
		r.Headers.Set(h, "not-a-session")
		r.Body = []byte(`{"prompt_cache_key":"shared","metadata":{"user_id":"user-1"},"previous_response_id":"resp-1"}`)
		if sessionKey(r) != "" {
			t.Fatalf("accepted unsafe identity: %s", h)
		}
	}
	for _, body := range []string{
		`{"metadata":{"user_id":"{\"session_id\":\"abc\"}"}}`,
		`{"metadata":{"user_id":"user_x_account_x_session_abcd-1234"}}`,
		`{"session_id":"abc"}`, `{"metadata":{"thread_id":"abc"}}`, `{"conversation":{"id":"abc"}}`,
	} {
		r := testRequest("")
		r.Body = []byte(body)
		if sessionKey(r) == "" {
			t.Fatalf("missed explicit session: %s", body)
		}
	}
	for _, id := range []string{"\n", strings.Repeat("x", 257)} {
		if sessionKey(testRequest(id)) != "" {
			t.Fatal("accepted invalid session ID")
		}
	}
	// Without host metadata, credentials still isolate callers.
	a.Metadata, b.Metadata = nil, nil
	b.Headers = a.Headers.Clone()
	a.Headers.Set("Authorization", "Bearer a")
	b.Headers.Set("Authorization", "Bearer b")
	if sessionKey(a) == sessionKey(b) {
		t.Fatal("credential scopes were merged")
	}
}

func TestConcurrentSessionDecisionAndReload(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	r, err := New(testConfig(), nil, func(_ context.Context, cfg Config, prompt string) Decision {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return Decision{Choice: "complex"}
	})
	if err != nil {
		t.Fatal(err)
	}
	const n = 40
	results := make(chan pluginapi.ModelRouteResponse, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- r.Route(testRequest("same")) }()
	}
	<-entered
	cfg := testConfig()
	cfg.Candidates[2].Model = "new-astra"
	if err := r.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	close(results)
	for result := range results {
		if result.TargetModel != "gpt-5.6-astra" || result.Target != "codex" || !result.Handled {
			t.Fatalf("binding changed: %+v", result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("Jev called %d times", calls.Load())
	}
	later := testRequest("same")
	later.Body = []byte(`{"input":[{"type":"function_call_output","output":"result"}]}`)
	later.SourceFormat, later.Stream = "openai-response", true
	if r.Route(later).TargetModel != "gpt-5.6-astra" || calls.Load() != 1 {
		t.Fatal("continuation was rerouted")
	}
	explicit := testRequest("same")
	explicit.RequestedModel = "gpt-5.6-sol"
	if r.Route(explicit).Handled || calls.Load() != 1 {
		t.Fatal("explicit model intercepted")
	}
	if got := r.Route(testRequest("new")); got.TargetModel != "new-astra" {
		t.Fatalf("new config unused: %+v", got)
	}
	cfg.Candidates[2].Model = "mutated-by-caller"
	if r.Route(testRequest("another")).TargetModel != "new-astra" {
		t.Fatal("config was not copied")
	}
	bad := testConfig()
	bad.Fallback = "absent"
	if r.Reconfigure(bad) == nil {
		t.Fatal("accepted invalid update")
	}
	if r.Route(testRequest("after-bad-update")).TargetModel != "new-astra" {
		t.Fatal("lost config on rejection")
	}
}

func TestFallbackAndProcessLifetime(t *testing.T) {
	var calls atomic.Int32
	decide := func(context.Context, Config, string) Decision { calls.Add(1); return Decision{Reason: "jev_timeout"} }
	r, _ := New(testConfig(), nil, decide)
	for i := 0; i < 2; i++ {
		if r.Route(testRequest("s")).TargetModel != "gpt-5.6-sol" {
			t.Fatal("wrong fallback")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("fallback not locked")
	}
	for i := 0; i < 2; i++ {
		r.Route(testRequest(""))
	}
	if calls.Load() != 1 || len(r.sessions) != 1 {
		t.Fatal("anonymous request cached or classified")
	}
	r2, _ := New(testConfig(), nil, decide)
	r2.Route(testRequest("s"))
	if calls.Load() != 2 {
		t.Fatal("new process did not reset state")
	}
	noText := testRequest("empty")
	noText.Body = []byte(`{"messages":[]}`)
	r.Route(noText)
	r.Route(testRequest("empty"))
	if calls.Load() != 2 {
		t.Fatal("missing prompt fallback not locked")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Candidates[0].Provider = "" },
		func(c *Config) { c.Candidates[0].Provider = "replace_with_provider" },
		func(c *Config) { c.Candidates[0].Key = "standard" },
		func(c *Config) { c.Candidates[0].Model = AutoModel },
		func(c *Config) { c.Fallback = "absent" },
		func(c *Config) { c.TimeoutMS = 0 },
	} {
		cfg := testConfig()
		mutate(&cfg)
		if cfg.Validate() == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
	data, _ := json.Marshal(testConfig())
	for _, text := range []string{string(data) + `{}`, `{"api_key":"secret"}`, `null`} {
		if _, err := DecodeConfig(strings.NewReader(text)); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
	cfg, err := DecodeConfig(strings.NewReader(string(data)))
	if err != nil || cfg.digest() != testConfig().digest() {
		t.Fatal("roundtrip failed", err)
	}
}
