package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const Version = "0.1.0"
const PluginID = "jev-adaptive-thinking"
const AutoModel = "jev-auto"

type Candidate struct {
	Key         string `json:"key"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Description string `json:"description"`
}

type Config struct {
	JevModel   string      `json:"jev_model"`
	TimeoutMS  int         `json:"timeout_ms"`
	Fallback   string      `json:"fallback"`
	Candidates []Candidate `json:"candidates"`
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("cannot open routing config")
	}
	defer f.Close()
	return DecodeConfig(io.LimitReader(f, 1<<20))
}

func DecodeConfig(r io.Reader) (Config, error) {
	c := Config{JevModel: "typesafe/jev-1.13", TimeoutMS: 1500, Fallback: "standard"}
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return Config{}, errors.New("invalid routing config JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("routing config must contain one JSON object")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.JevModel) == "" || c.TimeoutMS < 1 || c.TimeoutMS > 60000 {
		return errors.New("jev_model is required and timeout_ms must be between 1 and 60000")
	}
	if len(c.Candidates) < 1 {
		return errors.New("at least one candidate is required")
	}
	seen := make(map[string]bool)
	for i, candidate := range c.Candidates {
		if !keyPattern.MatchString(candidate.Key) || seen[candidate.Key] {
			return fmt.Errorf("candidate %d needs a unique lowercase key", i)
		}
		seen[candidate.Key] = true
		if !keyPattern.MatchString(candidate.Provider) || strings.HasPrefix(candidate.Provider, "replace_") {
			return fmt.Errorf("candidate %d needs an explicit host provider identifier", i)
		}
		if strings.TrimSpace(candidate.Model) == "" || candidate.Model == AutoModel || strings.TrimSpace(candidate.Description) == "" {
			return fmt.Errorf("candidate %d needs a target model and description", i)
		}
	}
	if !seen[c.Fallback] {
		return errors.New("fallback must reference a configured candidate key")
	}
	return nil
}

func (c Config) candidate(key string) (Candidate, bool) {
	for _, candidate := range c.Candidates {
		if candidate.Key == key {
			return candidate, true
		}
	}
	return Candidate{}, false
}

func (c Config) clone() Config {
	c.Candidates = append([]Candidate(nil), c.Candidates...)
	return c
}

func (c Config) digest() string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(bytes.TrimSpace(b))
	return hex.EncodeToString(sum[:])
}
