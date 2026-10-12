package cmd

// WSOpenCmd opens a workspace's agents with a launcher, as a fleet of one:
// each agent's terminal runs `ws run`, which checks its copies, runs it under
// its lease and leaves a shell in its replica.
type WSOpenCmd struct {
	Name     string `arg:"" help:"Workspace name." predictor:"workspace"`
	NoEnsure bool   `name:"no-ensure" help:"Don't make the workspace's missing copies first."`
	launchFlags
}

func (c *WSOpenCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	names := []string{c.Name}
	if !c.NoEnsure && !c.Print && cfg.FindWorkspace(c.Name) != nil {
		if err := ensureFleet(ctx, cfg, st, names); err != nil {
			return err
		}
	}
	plan, err := fleetPlan(ctx, cfg, st, c.Name, names, c.Agent, c.Cmd, c.Fresh)
	if err != nil {
		return err
	}
	return openPlan(ctx, cfg, st, plan, c.Launcher, c.launchFlags)
}
