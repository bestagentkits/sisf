package fakegithub

import (
	"errors"
	"fmt"
	"time"
)

// PullSpec describes a pull request to open. HeadRepo is "owner/name" of the
// fork the branch lives in, empty for a branch of the base repository.
type PullSpec struct {
	Title, Body      string
	HeadRef, BaseRef string
	HeadRepo         string
	Draft            bool
}

// OpenPullRequest opens a pull request as by and returns its number.
func (s *Server) OpenPullRequest(repoName string, by User, spec PullSpec) (int, error) {
	number := 0
	err := s.change(func() ([]pending, error) {
		r, err := s.repoFor(repoName)
		if err != nil {
			return nil, err
		}
		is, err := r.newPull(s.now(), by, spec)
		if err != nil {
			return nil, err
		}
		number = is.number
		return []pending{s.event(r, "pull_request", "opened", by, map[string]any{"pull_request": pullJSON(r, is)})}, nil
	})
	return number, err
}

func (r *repo) newPull(at time.Time, by User, spec PullSpec) (*issue, error) {
	if spec.HeadRef == "" || spec.BaseRef == "" {
		return nil, errors.New("a pull request needs a head and a base branch")
	}
	headOwner, headName := r.owner, r.name
	if spec.HeadRepo != "" {
		var err error
		if headOwner, headName, err = splitRepo(spec.HeadRepo); err != nil {
			return nil, err
		}
	}
	is := r.newIssue(at, by, spec.Title, spec.Body, nil)
	is.pr = &pull{
		headRef: spec.HeadRef, baseRef: spec.BaseRef, headOwner: headOwner, headName: headName, draft: spec.Draft,
		headSHA: fakeSHA(fmt.Sprintf("head:%s:%d", spec.HeadRef, is.number)), baseSHA: fakeSHA("base:" + spec.BaseRef),
	}
	return is, nil
}
