package main

import (
	"bytes"
	"context"

	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/managedapp"
	"github.com/bestagentkits/sisf/pkg/managedapp/changegate"
)

// commitReader reads one file of the factory's repository at a commit;
// stages.Tree is one.
type commitReader interface {
	ReadFile(ctx context.Context, commit core.CommitSHA, path string) ([]byte, error)
}

// managedChangeGate judges a managed application's candidate against the
// Owner-installed Spec and the lockfile at the base the candidate was
// accepted on. The application's directory is the repository root: the
// Spec's paths, the lockfile's included, are repository paths, so every
// changed path is judged as GitHub lists it and none is dropped.
type managedChangeGate struct {
	spec *managedapp.Spec
	tree commitReader
}

// Judge decides who may release the change from base to candidate. A
// lockfile that is missing or unreadable at either commit leaves the
// change to the Owner; only an outage of the repository is an error.
func (g managedChangeGate) Judge(ctx context.Context, base, candidate core.CommitSHA, changed []string) (changegate.Verdict, error) {
	in := changegate.Input{Baseline: g.spec, Changed: changed}
	if lock := g.spec.Bundle.Lock; lock != "" {
		var err error
		if in.BaselineLock, err = g.lock(ctx, base, lock); err != nil {
			return changegate.Verdict{}, err
		}
		if in.CandidateLock, err = g.lock(ctx, candidate, lock); err != nil {
			return changegate.Verdict{}, err
		}
	}
	return changegate.Judge(in), nil
}

// lock reads the lockfile at commit; nil when the commit has none or it
// does not parse, which the change gate gives to the Owner.
func (g managedChangeGate) lock(ctx context.Context, commit core.CommitSHA, path string) (*appbundle.Lock, error) {
	data, err := g.tree.ReadFile(ctx, commit, path)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	l, err := appbundle.ParseLock(bytes.NewReader(data))
	if err != nil {
		return nil, nil
	}
	return &l, nil
}
