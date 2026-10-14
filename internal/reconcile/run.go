package reconcile

import (
	"context"
	"errors"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/ashtonian/gitstead/internal/state"
)

// Apply records this run's observations, then runs the actions include
// selects and returns their results in plan order.
//
// A repo's actions run in plan order, and an action whose target already
// failed in this run is skipped, so a failed rename never leads to a move.
// Different repos' actions run at once, up to env.Concurrency doing disk work.
// Each action records what it did as soon as it has done it, and the records
// of actions that finish while a state write runs share the next one.
// Serial actions run after the others, one at a time.
func (p *Plan) Apply(ctx context.Context, env *Env, include func(Action) bool) []Result {
	r := &runner{
		env:    env,
		commit: state.NewGroupCommit(env.Update),
		slots:  make(chan struct{}, max(env.Concurrency, 1)),
	}
	if len(p.record) > 0 {
		// Each observation's disk work first, at most env.Concurrency at a
		// time; then their records, in one write.
		changes := make([]func(*state.State), len(p.record))
		var g errgroup.Group
		g.SetLimit(max(env.Concurrency, 1))
		for i, observe := range p.record {
			g.Go(func() error {
				changes[i] = observe(ctx)
				return nil
			})
		}
		_ = g.Wait()
		err := r.commit.Update(func(st *state.State) error {
			for _, change := range changes {
				if change != nil {
					change(st)
				}
			}
			return nil
		})
		if err != nil {
			return []Result{{Err: err}}
		}
	}
	for _, a := range p.Actions {
		if include(a) {
			r.actions = append(r.actions, a)
		}
	}
	r.results = make([]Result, len(r.actions))

	var targets []string
	chains := map[string][]int{}
	var serial []int
	for i, a := range r.actions {
		if a.Serial {
			serial = append(serial, i)
			continue
		}
		if chains[a.Target] == nil {
			targets = append(targets, a.Target)
		}
		chains[a.Target] = append(chains[a.Target], i)
	}

	var wg sync.WaitGroup
	for _, target := range targets {
		r.start(ctx, &wg, chains[target])
	}
	wg.Wait()
	r.start(ctx, &wg, serial)
	wg.Wait()
	return r.results
}

// runner runs a plan's actions. Its slots bound the disk work -- clones,
// git config writes -- running at once; an action gives up its slot while it
// waits for its record to be saved, so the actions behind it keep working
// and their records join the same write.
type runner struct {
	env     *Env
	commit  *state.GroupCommit
	slots   chan struct{}
	actions []Action
	results []Result
}

// start runs the actions at indexes chain, in order, in a new goroutine once
// a slot is free. Starting a chain only when a slot is free keeps the number
// of goroutines near the number of slots.
func (r *runner) start(ctx context.Context, wg *sync.WaitGroup, chain []int) {
	if len(chain) == 0 {
		return
	}
	s := &slot{r: r}
	if err := s.take(ctx); err != nil {
		for _, i := range chain {
			r.results[i] = Result{Action: r.actions[i], Err: err}
		}
		return
	}
	wg.Go(func() {
		defer s.give()
		failed := map[string]bool{}
		for _, i := range chain {
			a := r.actions[i]
			if failed[a.Target] {
				r.results[i] = Result{Action: a, Err: ErrSkipped}
				continue
			}
			info, err := r.run(ctx, s, a)
			r.results[i] = Result{Action: a, Info: info, Err: err}
			if err != nil {
				failed[a.Target] = true
			}
		}
	})
}

// run does a's disk work holding s, records it without, and finishes it.
func (r *runner) run(ctx context.Context, s *slot, a Action) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.take(ctx); err != nil {
		return "", err
	}
	out, err := a.Run(ctx, r.env)
	if out.Release != nil {
		defer out.Release()
	}
	if out.Record != nil {
		s.give()
		if rerr := r.commit.Update(out.Record); rerr != nil {
			return out.Info, errors.Join(err, rerr)
		}
	}
	if out.After == nil {
		return out.Info, err
	}
	if terr := s.take(ctx); terr != nil {
		return out.Info, errors.Join(err, terr)
	}
	info, aerr := out.After(ctx)
	return joinInfo(out.Info, info), errors.Join(err, aerr)
}

// slot is one chain's claim on a runner slot.
type slot struct {
	r    *runner
	held bool
}

func (s *slot) take(ctx context.Context) error {
	if s.held {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.r.slots <- struct{}{}:
		s.held = true
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *slot) give() {
	if s.held {
		<-s.r.slots
		s.held = false
	}
}

func joinInfo(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}
