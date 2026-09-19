package router

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event deliberately has no free-form message or request/response payload fields.
type Event struct {
	Time          string             `json:"time"`
	Level         string             `json:"level"`
	Event         string             `json:"event"`
	PID           int                `json:"pid"`
	Version       string             `json:"version"`
	Config        string             `json:"config_hash,omitempty"`
	RequestID     string             `json:"request_id,omitempty"`
	Session       string             `json:"session_hash,omitempty"`
	Protocol      string             `json:"protocol,omitempty"`
	Stream        bool               `json:"stream"`
	CacheHit      bool               `json:"cache_hit"`
	Choice        string             `json:"choice,omitempty"`
	Provider      string             `json:"provider,omitempty"`
	Model         string             `json:"model,omitempty"`
	ElapsedMS     float64            `json:"elapsed_ms"`
	Reason        string             `json:"reason,omitempty"`
	HTTPStatus    int                `json:"http_status,omitempty"`
	JevRequestID  string             `json:"jev_request_id,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type Logger struct {
	mu        sync.Mutex
	path      string
	file      *os.File
	stderr    io.Writer
	maxBytes  int64
	backups   int
	lastError map[string]time.Time
	closed    bool
}

func NewLogger() *Logger {
	home, err := os.UserHomeDir()
	l := newLogger(home, os.Stderr)
	if err != nil {
		l.path = ""
		l.report("home_unavailable")
	}
	return l
}

func newLogger(home string, stderr io.Writer) *Logger {
	l := &Logger{
		stderr: stderr, maxBytes: 10 << 20, backups: 5,
		lastError: make(map[string]time.Time),
	}
	if home != "" {
		l.path = filepath.Join(home, ".local", "state", PluginID, "logs", "router.jsonl")
	}
	return l
}

func (l *Logger) Path() string { return l.path }

func (l *Logger) Log(e Event) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	e.PID, e.Version = os.Getpid(), Version
	if e.Level == "" {
		e.Level = "info"
	}
	line, err := json.Marshal(e)
	if err != nil {
		l.report("encode_failed")
		return
	}
	line = append(line, '\n')
	if l.file == nil && !l.open() {
		return
	}
	stat, err := l.file.Stat()
	if err != nil {
		l.report("stat_failed")
		return
	}
	if stat.Size() > 0 && stat.Size()+int64(len(line)) > l.maxBytes {
		if !l.rotate() {
			return
		}
	}
	if _, err := l.file.Write(line); err != nil {
		l.report("write_failed")
		l.file.Close()
		l.file = nil
	}
}

func (l *Logger) open() bool {
	if l.path == "" {
		l.report("home_unavailable")
		return false
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		l.report("mkdir_failed")
		return false
	}
	if err := os.Chmod(dir, 0700); err != nil {
		l.report("permissions_failed")
		return false
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		l.report("open_failed")
		return false
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		l.report("permissions_failed")
		return false
	}
	l.file = f
	return true
}

func (l *Logger) rotate() bool {
	if err := l.file.Close(); err != nil {
		l.file = nil
		l.report("close_failed")
		return false
	}
	l.file = nil
	for i := l.backups; i >= 1; i-- {
		from := l.path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", l.path, i-1)
		}
		to := fmt.Sprintf("%s.%d", l.path, i)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			l.report("rotate_failed")
			return false
		}
	}
	return l.open()
}

// Call only while holding mu (or during construction). Do not print OS error
// strings: they can contain attacker-controlled filenames or other data.
func (l *Logger) report(reason string) {
	now := time.Now()
	if now.Sub(l.lastError[reason]) < time.Minute {
		return
	}
	l.lastError[reason] = now
	fmt.Fprintf(l.stderr, "%s: log error=%s path=%q\n", PluginID, reason, l.path)
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			l.report("close_failed")
		}
		l.file = nil
	}
}
