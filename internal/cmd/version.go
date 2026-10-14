package cmd

import "fmt"

// VersionCmd prints version information.
type VersionCmd struct {
	Version string `kong:"-"` // injected, not a flag
}

func (v *VersionCmd) Run(ctx *Context) error {
	fmt.Println("gitten " + ctx.VersionStr)
	return ctx.finish(map[string]string{"version": ctx.VersionStr}, nil)
}
