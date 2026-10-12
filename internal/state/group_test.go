package state

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

func TestGroupCommit_SharesWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := &State{Repos: map[string]*RepoState{}}
		release := make(chan struct{})
		writes := 0
		g := NewGroupCommit(func(change func(*State) error) error {
			writes++
			if writes == 1 {
				<-release
			}
			return change(st)
		})
		track := func(key string) func(*State) error {
			return func(st *State) error {
				st.Repos[key] = &RepoState{}
				return nil
			}
		}
		refused := errors.New("no longer tracked")

		errs := make([]error, 4)
		var wg sync.WaitGroup
		wg.Go(func() { errs[0] = g.Update(track("first")) })
		synctest.Wait() // the first write is under way
		wg.Go(func() { errs[1] = g.Update(track("a")) })
		wg.Go(func() { errs[2] = g.Update(func(*State) error { return refused }) })
		wg.Go(func() { errs[3] = g.Update(track("b")) })
		synctest.Wait() // and the other three wait for it
		close(release)
		wg.Wait()

		if writes != 2 {
			t.Errorf("%d writes; the changes made during the first write should share the second", writes)
		}
		if errs[0] != nil || errs[1] != nil || errs[3] != nil || !errors.Is(errs[2], refused) {
			t.Errorf("errors = %v; only the change that failed should get one", errs)
		}
		if len(st.Repos) != 3 {
			t.Errorf("repos = %v; the failed change's batch should still be saved", st.Repos)
		}
	})
}

func TestGroupCommit_FailedWriteFailsItsChanges(t *testing.T) {
	full := errors.New("disk full")
	fail := true
	g := NewGroupCommit(func(change func(*State) error) error {
		if err := change(&State{}); err != nil {
			return err
		}
		if fail {
			return full
		}
		return nil
	})
	if err := g.Update(func(*State) error { return nil }); !errors.Is(err, full) {
		t.Errorf("Update = %v, want the write's error", err)
	}
	fail = false
	if err := g.Update(func(*State) error { return nil }); err != nil {
		t.Errorf("after a failed write, Update = %v", err)
	}
}
