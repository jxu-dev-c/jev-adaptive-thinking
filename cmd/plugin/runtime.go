package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"

	"jev-adaptive-thinking/internal/router"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type pluginRuntime struct {
	mu     sync.RWMutex
	router *router.Router
	logger *router.Logger
	newLog func() *router.Logger
	closed bool
}

func newPluginRuntime() *pluginRuntime {
	return &pluginRuntime{newLog: router.NewLogger}
}

func registration() any {
	return struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  struct {
			ModelRouter bool `json:"model_router"`
		} `json:"capabilities"`
	}{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name: router.PluginID, Version: router.Version,
			ConfigFields: []pluginapi.ConfigField{{
				Name: "config_path", Type: pluginapi.ConfigFieldTypeString,
				Description: "Absolute path to the Jev routing JSON configuration.",
			}},
		},
		Capabilities: struct {
			ModelRouter bool `json:"model_router"`
		}{true},
	}
}

func (p *pluginRuntime) handle(method string, raw []byte) (any, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return p.configure(raw)
	case pluginabi.MethodPluginShutdown:
		p.shutdown()
		return struct{}{}, nil
	case pluginabi.MethodPluginQuiesce:
		// Taking the exclusive lock drains already-entered model.route calls.
		p.mu.Lock()
		p.mu.Unlock()
		return struct{}{}, nil
	case pluginabi.MethodModelRoute:
		p.mu.RLock()
		defer p.mu.RUnlock()
		if p.closed || p.router == nil {
			return nil, errors.New("plugin is not configured")
		}
		var req pluginapi.ModelRouteRequest
		if json.Unmarshal(raw, &req) != nil {
			return nil, errors.New("invalid model.route request")
		}
		return p.router.Route(req), nil
	default:
		return nil, errors.New("unsupported plugin method")
	}
}

func (p *pluginRuntime) configure(raw []byte) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("plugin is shut down")
	}
	if p.logger == nil {
		p.logger = p.newLog()
	}
	reject := func(err error) (any, error) {
		p.logger.Log(router.Event{Event: "config_rejected", Level: "error", Reason: "invalid_config"})
		return nil, err
	}
	var lifecycle struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if json.Unmarshal(raw, &lifecycle) != nil {
		return reject(errors.New("invalid lifecycle request"))
	}
	var settings struct {
		ConfigPath string `yaml:"config_path"`
	}
	if yaml.Unmarshal(lifecycle.ConfigYAML, &settings) != nil || !filepath.IsAbs(settings.ConfigPath) {
		return reject(errors.New("config_path must be an absolute path in plugin configuration"))
	}
	cfg, err := router.LoadConfig(settings.ConfigPath)
	if err != nil {
		return reject(err)
	}
	if p.router == nil {
		p.router, err = router.New(cfg, p.logger, nil)
	} else {
		err = p.router.Reconfigure(cfg)
	}
	if err != nil {
		return reject(err)
	}
	return registration(), nil
}

func (p *pluginRuntime) shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.logger.Log(router.Event{Event: "shutdown"})
	p.logger.Close()
}
