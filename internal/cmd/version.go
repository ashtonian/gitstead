package cmd

import "fmt"

// VersionCmd prints version information.
type VersionCmd struct {
	Version string `kong:"-"` // injected, not a flag
}

func (v *VersionCmd) Run(ctx *Context) error {
	fmt.Println("git-mgr " + ctx.VersionStr)
	return nil
}
