package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"jev-adaptive-thinking/internal/router"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRuntimeLifecycle(t *testing.T) {
	t.Setenv("OPEN_ROUTER_API_KEY", "")
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig := func(model string) {
		t.Helper()
		data := `{"candidates":[{"key":"standard","provider":"codex","model":"` + model + `","description":"routine"}]}`
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("gpt-5.6-sol")
	p := newPluginRuntime()
	// Logger behavior is covered separately with explicit temporary directories.
	p.newLog = func() *router.Logger { return nil }
	lifecycle, _ := json.Marshal(map[string]any{"config_yaml": []byte("config_path: " + path)})
	reg, err := p.handle(pluginabi.MethodPluginRegister, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(reg)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &fields)
	if string(fields["capabilities"]) != `{"model_router":true}` {
		t.Fatalf("wrong capabilities: %s", encoded)
	}
	req := pluginapi.ModelRouteRequest{RequestedModel: "jev-auto", SourceFormat: "openai", Headers: map[string][]string{"Session-Id": {"session"}}, Body: []byte(`{"messages":[{"role":"user","content":"test"}]}`)}
	callRoute := func() pluginapi.ModelRouteResponse {
		t.Helper()
		raw, _ := json.Marshal(req)
		out, err := p.handle(pluginabi.MethodModelRoute, raw)
		if err != nil {
			t.Fatal(err)
		}
		return out.(pluginapi.ModelRouteResponse)
	}
	if got := callRoute(); got.TargetModel != "gpt-5.6-sol" {
		t.Fatal(got)
	}
	writeConfig("new-model")
	if _, err := p.handle(pluginabi.MethodPluginReconfigure, lifecycle); err != nil {
		t.Fatal(err)
	}
	if got := callRoute(); got.TargetModel != "gpt-5.6-sol" {
		t.Fatal("reload changed binding", got)
	}
	if err := os.WriteFile(path, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.handle(pluginabi.MethodPluginReconfigure, lifecycle); err == nil {
		t.Fatal("accepted invalid config")
	}
	req.Headers["Session-Id"] = []string{"new-session"}
	if got := callRoute(); got.TargetModel != "new-model" {
		t.Fatal("lost valid config", got)
	}
	if _, err := p.handle(pluginabi.MethodPluginQuiesce, nil); err != nil {
		t.Fatal(err)
	}
	p.shutdown()
	p.shutdown()
	if _, err := p.handle(pluginabi.MethodModelRoute, []byte(`{}`)); err == nil {
		t.Fatal("accepted call after shutdown")
	}
}
