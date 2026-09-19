package router

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type binding struct {
	ready     chan struct{}
	candidate Candidate
	reason    string
	config    string
}

type Router struct {
	mu       sync.Mutex
	config   Config
	sessions map[string]*binding
	decide   DecideFunc
	log      *Logger
	sequence atomic.Uint64
}

func New(cfg Config, log *Logger, decide DecideFunc) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if decide == nil {
		decide = NewJevClient().Decide
	}
	r := &Router{config: cfg.clone(), sessions: make(map[string]*binding), log: log, decide: decide}
	log.Log(Event{Event: "initialized", Config: cfg.digest()})
	return r, nil
}

func (r *Router) Reconfigure(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		r.log.Log(Event{Event: "config_rejected", Level: "error", Reason: "invalid_config"})
		return err
	}
	r.mu.Lock()
	r.config = cfg.clone()
	r.mu.Unlock()
	r.log.Log(Event{Event: "config_loaded", Config: cfg.digest()})
	return nil
}

func (r *Router) Route(req pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
	if req.RequestedModel != AutoModel {
		return pluginapi.ModelRouteResponse{Handled: false}
	}
	start := time.Now()
	event := Event{
		RequestID: fmt.Sprintf("%d-%d", start.UnixNano(), r.sequence.Add(1)),
		Session:   sessionKey(req), Protocol: protocol(req.SourceFormat), Stream: req.Stream,
	}
	r.mu.Lock()
	cfg := r.config.clone()
	if event.Session == "" {
		r.mu.Unlock()
		c, _ := cfg.candidate(cfg.Fallback)
		return r.finish(event, c, "missing_session", cfg.digest(), start)
	}
	if existing, ok := r.sessions[event.Session]; ok {
		r.mu.Unlock()
		<-existing.ready
		event.CacheHit = true
		return r.finish(event, existing.candidate, existing.reason, existing.config, start)
	}
	entry := &binding{ready: make(chan struct{}), config: cfg.digest()}
	r.sessions[event.Session] = entry
	r.mu.Unlock()

	// All paths publish exactly one immutable binding, including failure fallback.
	decision := r.firstDecision(req, cfg)
	candidate, valid := cfg.candidate(decision.Choice)
	if decision.Reason != "" || !valid {
		candidate, _ = cfg.candidate(cfg.Fallback)
		if decision.Reason == "" {
			decision.Reason = "jev_unknown_choice"
		}
	}
	entry.candidate, entry.reason = candidate, decision.Reason
	close(entry.ready)
	event.HTTPStatus, event.JevRequestID = decision.HTTPStatus, decision.RequestID
	event.Probabilities = decision.Probabilities
	return r.finish(event, candidate, decision.Reason, entry.config, start)
}

func (r *Router) firstDecision(req pluginapi.ModelRouteRequest, cfg Config) (decision Decision) {
	// Keep the per-session promise resolvable even if a dependency panics. The
	// recovered value can contain request data and must not reach diagnostics.
	defer func() {
		if recover() != nil {
			decision = Decision{Reason: "decision_panic"}
		}
	}()
	if protocol(req.SourceFormat) == "unsupported" {
		return Decision{Reason: "unsupported_protocol"}
	}
	prompt, reason := firstPrompt(req.Body, req.SourceFormat)
	if reason != "" {
		return Decision{Reason: reason}
	}
	return r.decide(context.Background(), cfg, prompt)
}

func (r *Router) finish(event Event, candidate Candidate, reason, config string, start time.Time) pluginapi.ModelRouteResponse {
	event.Config, event.Choice, event.Provider, event.Model = config, candidate.Key, candidate.Provider, candidate.Model
	event.Reason, event.ElapsedMS = reason, float64(time.Since(start).Microseconds())/1000
	event.Event = "decision"
	if event.CacheHit {
		event.Event = "cache_hit"
	} else if reason != "" {
		event.Event, event.Level = "fallback", "warn"
	}
	r.log.Log(event)
	routeReason := "jev_selected_" + candidate.Key
	if reason != "" {
		routeReason = "jev_fallback_" + reason
	}
	return pluginapi.ModelRouteResponse{
		Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider,
		Target: candidate.Provider, TargetModel: candidate.Model, Reason: routeReason,
	}
}
