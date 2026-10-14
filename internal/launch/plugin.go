package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// Plugin is a launcher outside gitstead: an executable gitstead-launch-<name>
// on PATH, given the launch plan as JSON on stdin, with its options from
// launchers.plugins.<name>. It has the terminal, and its exit status is the
// launch's.
type Plugin struct {
	Launcher string
	Path     string
	Options  map[string]any
	Env      Env
}

func (p *Plugin) Name() string { return p.Launcher }

func (p *Plugin) Open(ctx context.Context, plan Plan) error {
	plan.Options = p.Options
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, p.Path)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout, cmd.Stderr = p.Env.Stdout, p.Env.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", p.Path, err)
	}
	return nil
}
