package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
