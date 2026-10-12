package reconcile

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ashtonian/git-mgr/internal/state"
)

// recordOnly is an action with no disk work that records key.
func recordOnly(target string) Action {
	return Action{Kind: KindTags, Target: target, run: func(context.Context, *Env) (outcome, error) {
		return outcome{record: func(st *state.State) error {
			st.Repos[target] = &state.RepoState{}
			return nil
		}}, nil
	}}
}

func failing(target string, err error) Action {
	return Action{Kind: KindURL, Target: target, run: func(context.Context, *Env) (outcome, error) {
		return outcome{}, err
	}}
}

func memEnv(concurrency int) (*Env, *state.State) {
	st := &state.State{Repos: map[string]*state.RepoState{}}
	var mu sync.Mutex
	return &Env{State: st, Concurrency: concurrency, Update: func(change func(*state.State) error) error {
		mu.Lock()
		defer mu.Unlock()
		return change(st)
	}}, st
}

func TestApply_SkipsTheRestOfAFailedTarget(t *testing.T) {
	refused := errors.New("refused")
	p := &Plan{Actions: []Action{failing("a", refused), recordOnly("b"), recordOnly("a"), recordOnly("c")}}
	env, st := memEnv(2)

	results := p.Apply(context.Background(), env, All)

	want := []struct {
		target string
		err    error
	}{{"a", refused}, {"b", nil}, {"a", ErrSkipped}, {"c", nil}}
	if len(results) != len(want) {
		t.Fatalf("results = %+v", results)
	}
	for i, w := range want {
		if results[i].Action.Target != w.target || !errors.Is(results[i].Err, w.err) || (w.err == nil) != (results[i].Err == nil) {
			t.Errorf("result %d = %s %v, want %s %v (results stay in plan order)", i, results[i].Action.Target, results[i].Err, w.target, w.err)
		}
	}
	if st.Repos["a"] != nil || st.Repos["b"] == nil || st.Repos["c"] == nil {
		t.Errorf("recorded %v; want b and c, and nothing for a after its failure", st.Repos)
	}
}

// With one slot, the records of three repos' actions share writes only if an
// action waiting for its record to be saved gives up its slot: the first
// write holds until the other two actions have done their work.
func TestApply_ActionsWorkWhileARecordIsSaved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := &state.State{Repos: map[string]*state.RepoState{}}
		release := make(chan struct{})
		writes := 0
		env := &Env{State: st, Concurrency: 1, Update: func(change func(*state.State) error) error {
			writes++
			if writes == 1 {
				<-release
			}
			return change(st)
		}}
		p := &Plan{Actions: []Action{recordOnly("a"), recordOnly("b"), recordOnly("c")}}

		var results []Result
		done := make(chan struct{})
		go func() {
			results = p.Apply(context.Background(), env, All)
			close(done)
		}()
		synctest.Wait()
		close(release)
		<-done

		for _, r := range results {
			if r.Err != nil {
				t.Errorf("%s: %v", r.Action.Target, r.Err)
			}
		}
		if writes != 2 {
			t.Errorf("%d writes; b's and c's records should share the write after a's", writes)
		}
	})
}

func TestApply_StopsStartingWorkWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env, st := memEnv(1)
	results := (&Plan{Actions: []Action{recordOnly("a"), recordOnly("b")}}).Apply(ctx, env, All)
	for _, r := range results {
		if !errors.Is(r.Err, context.Canceled) {
			t.Errorf("%s: err = %v, want it cancelled", r.Action.Target, r.Err)
		}
	}
	if len(st.Repos) != 0 {
		t.Errorf("recorded %v after cancelling", st.Repos)
	}
}

// BenchmarkApply_Records applies a state-only change to each of 1,000 tracked
// repos, saved to a state file: the write cost every apply action pays.
func BenchmarkApply_Records(b *testing.B) {
	const n = 1000
	st, err := state.Load(filepath.Join(b.TempDir(), "state.yaml"))
	if err != nil {
		b.Fatal(err)
	}
	var actions []Action
	for i := range n {
		key := fmt.Sprintf("github.com/owner/repo-%d", i)
		st.Repos[key] = &state.RepoState{Host: "github.com", Owner: "owner", Name: fmt.Sprintf("repo-%d", i), FullName: key[len("github.com/"):],
			LocalPath: "/Users/me/projects/" + key, CloneURL: "git@github.com:" + key + ".git", Cloned: true, Tags: []string{"work"}}
		actions = append(actions, Action{Kind: KindTags, Target: key, run: retag(Repo{Key: key, Tags: []string{"work", "go"}})})
	}
	if err := st.Save(); err != nil {
		b.Fatal(err)
	}
	writes := 0
	env := &Env{State: st, Concurrency: 8, Update: func(change func(*state.State) error) error {
		writes++
		return st.Update(time.Minute, change)
	}}
	for b.Loop() {
		for _, r := range (&Plan{Actions: actions}).Apply(context.Background(), env, All) {
			if r.Err != nil {
				b.Fatal(r.Err)
			}
		}
	}
	b.ReportMetric(float64(writes)/float64(b.N), "writes/op")
}
