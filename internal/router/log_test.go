package router

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLogPathAppendPermissionsAndConcurrency(t *testing.T) {
	home := t.TempDir()
	l := newLogger(home, io.Discard)
	want := filepath.Join(home, ".local/state/jev-adaptive-thinking/logs/router.jsonl")
	if l.Path() != want {
		t.Fatalf("path=%s", l.Path())
	}
	l.Log(Event{Event: "initialized"})
	l.Close()
	l = newLogger(home, io.Discard)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); l.Log(Event{Event: "cache_hit", Session: "digest"}) }()
	}
	wg.Wait()
	l.Close()
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	s := bufio.NewScanner(bytes.NewReader(data))
	n := 0
	for s.Scan() {
		var e Event
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			t.Fatal("broken JSONL", err)
		}
		if e.Time == "" || e.PID == 0 || e.Version != Version {
			t.Fatal("missing diagnostic fields")
		}
		n++
	}
	if n != 101 {
		t.Fatalf("lines=%d", n)
	}
	for path, mode := range map[string]os.FileMode{want: 0600, filepath.Dir(want): 0700} {
		stat, err := os.Stat(path)
		if err != nil || stat.Mode().Perm() != mode {
			t.Fatalf("permissions for %s: %v %v", path, stat, err)
		}
	}
}

func TestLogRotationAndFailureReporting(t *testing.T) {
	l := newLogger(t.TempDir(), io.Discard)
	l.maxBytes = 1 // Every additional event triggers a rotation.
	for i := 0; i < 12; i++ {
		l.Log(Event{Event: "decision"})
	}
	l.Close()
	files, err := filepath.Glob(l.Path() + "*")
	if err != nil || len(files) != 6 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil || !json.Valid(bytes.TrimSpace(data)) {
			t.Fatalf("invalid backup %s", path)
		}
	}
	var stderr bytes.Buffer
	brokenHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenHome, ".local"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := newLogger(brokenHome, &stderr)
	for i := 0; i < 5; i++ {
		broken.Log(Event{Event: "decision"})
	}
	if strings.Count(stderr.String(), "mkdir_failed") != 1 || !strings.Contains(stderr.String(), broken.Path()) {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	r, _ := New(testConfig(), broken, nil)
	if r.Route(testRequest("")).TargetModel != "gpt-5.6-sol" {
		t.Fatal("logging failure interrupted routing")
	}
}

func TestLogsExcludeSensitiveData(t *testing.T) {
	l := newLogger(t.TempDir(), io.Discard)
	r, _ := New(testConfig(), l, nil)
	req := testRequest("private-session-id")
	req.Headers.Set("Authorization", "Bearer secret-client-key")
	req.Body = []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"data":"private-image"}}]}]}`)
	r.Route(req)
	r.Route(req)
	l.Close()
	data, _ := os.ReadFile(l.Path())
	for _, secret := range []string{"private-session-id", "secret-client-key", "private-image", "Authorization"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if !bytes.Contains(data, []byte("multimodal_prompt")) || !bytes.Contains(data, []byte("cache_hit")) {
		t.Fatal("missing route diagnostics")
	}
}
