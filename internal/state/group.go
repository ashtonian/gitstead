package state

import "sync"

// GroupCommit shares state writes among concurrent callers. Every write
// rewrites the whole file, so its cost grows with the number of tracked
// repos; the changes that arrive while one write runs are saved together by
// the next, instead of one write each.
type GroupCommit struct {
	update func(change func(*State) error) error

	mu      sync.Mutex
	writing bool
	pending []*groupChange
}

type groupChange struct {
	change func(*State) error
	err    error
	done   bool
	// wake is closed when the change is saved, or when its caller is to write
	// the next batch.
	wake chan struct{}
}

// NewGroupCommit returns a GroupCommit that writes through update, such as a
// State's Update with its lock timeout.
func NewGroupCommit(update func(change func(*State) error) error) *GroupCommit {
	return &GroupCommit{update: update}
}

// Update applies change to the latest state and returns once it is saved,
// sharing the write with the changes other callers make meanwhile. A change
// that fails gets its own error, and must leave the state as it found it:
// the other changes in its write are saved.
func (g *GroupCommit) Update(change func(*State) error) error {
	c := &groupChange{change: change, wake: make(chan struct{})}
	g.mu.Lock()
	g.pending = append(g.pending, c)
	if g.writing {
		g.mu.Unlock()
		<-c.wake
		if c.done {
			return c.err
		}
	} else {
		g.writing = true
		g.mu.Unlock()
	}
	g.write(c)
	return c.err
}

// write saves every pending change in one update, then hands the next write
// to the first caller still waiting.
func (g *GroupCommit) write(self *groupChange) {
	g.mu.Lock()
	batch := g.pending
	g.pending = nil
	g.mu.Unlock()

	err := g.update(func(st *State) error {
		for _, c := range batch {
			c.err = c.change(st)
		}
		return nil
	})

	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range batch {
		if c.err == nil {
			c.err = err
		}
		c.done = true
		if c != self {
			close(c.wake)
		}
	}
	if len(g.pending) > 0 {
		close(g.pending[0].wake)
	} else {
		g.writing = false
	}
}
