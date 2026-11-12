package fakegithub

import "slices"

func issueJSON(is *issue) jIssue {
	out := jIssue{
		Number: is.number, Title: is.title, Body: is.body, State: is.state, Labels: []jLabel{},
		User: is.author.json(), AuthorAssociation: is.author.assoc(), CreatedAt: is.created, UpdatedAt: is.updated,
	}
	for _, l := range is.labels {
		out.Labels = append(out.Labels, jLabel{Name: l})
	}
	if is.pr != nil {
		out.PullRequest = &struct{}{}
	}
	return out
}

func commentJSON(c *comment) jComment {
	return jComment{ID: c.id, Body: c.body, User: c.author.json(), AuthorAssociation: c.author.assoc(), CreatedAt: c.created, UpdatedAt: c.updated}
}

func pullJSON(r *repo, is *issue) jPull {
	head := repoJSON(is.pr.headOwner, is.pr.headName)
	base := repoJSON(r.owner, r.name)
	return jPull{
		Number: is.number, Title: is.title, Body: is.body, State: is.state, Draft: is.pr.draft, Merged: is.pr.merged,
		Head: jBranch{Ref: is.pr.headRef, SHA: is.pr.headSHA, Repo: &head},
		Base: jBranch{Ref: is.pr.baseRef, SHA: is.pr.baseSHA, Repo: &base},
		User: is.author.json(), AuthorAssociation: is.author.assoc(), CreatedAt: is.created, UpdatedAt: is.updated,
	}
}

// IssueView is a read-only copy of an issue for assertions.
type IssueView struct {
	Number int
	Title  string
	Body   string
	State  string
	Labels []string
	Author User
}

// Issue returns a copy of an issue or pull request.
func (s *Server) Issue(repoName string, number int) (IssueView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, is, err := s.find(repoName, number)
	if err != nil {
		return IssueView{}, err
	}
	return IssueView{Number: is.number, Title: is.title, Body: is.body, State: is.state, Labels: slices.Clone(is.labels), Author: is.author}, nil
}

// CommentView is a read-only copy of a comment for assertions.
type CommentView struct {
	ID     int64
	Body   string
	Author User
}

// Comments returns the comments of an issue, oldest first.
func (s *Server) Comments(repoName string, number int) ([]CommentView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, _, err := s.find(repoName, number)
	if err != nil {
		return nil, err
	}
	var out []CommentView
	for _, c := range r.comments {
		if c.issue == number {
			out = append(out, CommentView{ID: c.id, Body: c.body, Author: c.author})
		}
	}
	return out, nil
}

// CheckRuns returns the check runs published to a repository as "name:conclusion".
func (s *Server) CheckRuns(repoName string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.repoFor(repoName)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range r.checks {
		out = append(out, c.Name+":"+c.Conclusion)
	}
	return out, nil
}

// Releases returns the tags released in a repository.
func (s *Server) Releases(repoName string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.repoFor(repoName)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rel := range r.releases {
		out = append(out, rel.Tag)
	}
	return out, nil
}
