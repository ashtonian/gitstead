package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/gitten/internal/config"
)

// A command run as a step of another hands back its document instead of
// printing it, so the outer command prints one.
func TestCaptured(t *testing.T) {
	var out bytes.Buffer
	ctx := &Context{JSON: true, Out: &out}
	boom := errors.New("boom")
	doc, err := ctx.captured(func(c *Context) error {
		return c.finish(map[string]int{"n": 1}, boom)
	})
	if !errors.Is(err, boom) || out.Len() != 0 {
		t.Fatalf("err %v, printed %q", err, out.String())
	}
	var got map[string]int
	if json.Unmarshal(doc, &got) != nil || got["n"] != 1 {
		t.Errorf("doc = %s", doc)
	}
	if err := ctx.finish(map[string]json.RawMessage{"step": doc}, nil); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) {
		t.Errorf("printed %q", out.String())
	}
}

// Without --json, finish prints nothing and returns the command's error.
func TestFinish_Text(t *testing.T) {
	var out bytes.Buffer
	ctx := &Context{Out: &out}
	if err := ctx.finish(map[string]int{"n": 1}, nil); err != nil || out.Len() != 0 {
		t.Errorf("err %v, printed %q", err, out.String())
	}
}

// Whatever a command puts in its JSON result, a credential in a URL is
// never printed.
func TestEmit_RedactsCredentials(t *testing.T) {
	var out bytes.Buffer
	c := &Context{JSON: true, Out: &out}
	doc := map[string]string{"remote": "https://alice:not-a-real-token@example.invalid/o/r.git"}
	if err := c.Emit(doc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "not-a-real-token") || !strings.Contains(out.String(), "https://***@example.invalid/o/r.git") {
		t.Errorf("emitted:\n%s", out.String())
	}
}

// A run under --json ends with exactly one document on stdout: the
// command's, else its error, else an empty one.
func TestConclude(t *testing.T) {
	boom := &exitError{exitPartial, errors.New("boom")}
	tests := []struct {
		name   string
		emit   bool
		err    error
		want   string
		wantRC int
	}{
		{"the command's document", true, boom, `{"done":true}`, exitPartial},
		{"the error, when it printed none", false, boom, `{"error":"boom","exit_code":3}`, exitPartial},
		{"an empty one for a success that printed none", false, nil, `{}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			c := &Context{JSON: true, Out: &out}
			if tt.emit {
				if err := c.Emit(map[string]bool{"done": true}); err != nil {
					t.Fatal(err)
				}
			}
			if rc := Conclude(c, tt.err); rc != tt.wantRC {
				t.Errorf("exit code %d, want %d", rc, tt.wantRC)
			}
			var got bytes.Buffer
			if err := json.Compact(&got, out.Bytes()); err != nil {
				t.Fatalf("stdout isn't one document: %v\n%s", err, out.String())
			}
			if got.String() != tt.want {
				t.Errorf("stdout = %s, want %s", got.String(), tt.want)
			}
		})
	}
}

func TestWantsJSON(t *testing.T) {
	for args, want := range map[string]bool{
		"--json status":       true,
		"status --json=true":  true,
		"run -- echo --json":  false,
		"status --jsonx":      false,
		"ws run app agent-1 ": false,
	} {
		if got := WantsJSON(strings.Fields(args)); got != want {
			t.Errorf("WantsJSON(%q) = %v, want %v", args, got, want)
		}
	}
}

// A resolved section prints as config spells it, keys and durations alike.
func TestAsConfigValue(t *testing.T) {
	v, err := asConfigValue(config.SyncConfig{TimeoutPerRepo: 5 * time.Minute, Retries: 2})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(v)
	if got := string(data); !strings.Contains(got, `"timeout_per_repo":"5m0s"`) || !strings.Contains(got, `"retries":2`) {
		t.Errorf("sync section = %s", got)
	}
}
