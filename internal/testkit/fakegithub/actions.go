package fakegithub

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// errNotFound marks a missing repository, issue or comment; the API answers 404.
var errNotFound = errors.New("not found")

// change runs fn under the state lock and then delivers the webhooks it
// produced with no lock held, so a receiver may call the API back.
func (s *Server) change(fn func() ([]pending, error)) error {
	s.mu.Lock()
	events, err := fn()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.dispatch(events)
	return nil
}

// AddRepo creates an empty repository "owner/name".
func (s *Server) AddRepo(full string) error {
	owner, name, err := splitRepo(full)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.repos[repoKey(owner, name)]; !ok {
		s.repos[repoKey(owner, name)] = &repo{owner: owner, name: name, nextNumber: 1, issues: map[int]*issue{}}
	}
	return nil
}

// Bot returns the account API calls act as by default.
func (s *Server) Bot() User { return s.bot }

// Person returns the account for login, creating it on first use. The id is
// stable, so the same login is always the same account.
func (s *Server) Person(login, association string) User {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[login]
	if !ok {
		s.nextUserID++
		u = User{ID: s.nextUserID, Login: login}
	}
	u.Association = association
	s.users[login] = u
	return u
}

// repoFor finds a repository. Callers hold s.mu.
func (s *Server) repoFor(full string) (*repo, error) {
	owner, name, err := splitRepo(full)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotFound, err)
	}
	r, ok := s.repos[repoKey(owner, name)]
	if !ok {
		return nil, fmt.Errorf("%w: repository %s", errNotFound, full)
	}
	return r, nil
}

func (r *repo) issue(number int) (*issue, error) {
	is, ok := r.issues[number]
	if !ok {
		return nil, fmt.Errorf("%w: issue #%d", errNotFound, number)
	}
	return is, nil
}

// OpenIssue creates an issue as by and returns its number.
func (s *Server) OpenIssue(repoName string, by User, title, body string, labels ...string) (int, error) {
	number := 0
	err := s.change(func() ([]pending, error) {
		r, err := s.repoFor(repoName)
		if err != nil {
			return nil, err
		}
		is := r.newIssue(s.now(), by, title, body, labels)
		number = is.number
		return []pending{s.event(r, "issues", "opened", by, map[string]any{"issue": issueJSON(is)})}, nil
	})
	return number, err
}

func (r *repo) newIssue(at time.Time, by User, title, body string, labels []string) *issue {
	is := &issue{number: r.nextNumber, title: title, body: body, state: "open", labels: slices.Clone(labels), author: by, created: at, updated: at}
	r.issues[is.number] = is
	r.nextNumber++
	return is
}

// SetIssueState opens or closes an issue or pull request as by.
func (s *Server) SetIssueState(repoName string, number int, by User, state string) error {
	if state != "open" && state != "closed" {
		return fmt.Errorf("state %q must be open or closed", state)
	}
	return s.change(func() ([]pending, error) {
		r, is, err := s.find(repoName, number)
		if err != nil {
			return nil, err
		}
		if is.state == state {
			return nil, nil
		}
		is.state, is.updated = state, s.now()
		action, kind := map[string]string{"open": "reopened", "closed": "closed"}[state], "issues"
		fields := map[string]any{"issue": issueJSON(is)}
		if is.pr != nil {
			kind, fields = "pull_request", map[string]any{"pull_request": pullJSON(r, is)}
		}
		return []pending{s.event(r, kind, action, by, fields)}, nil
	})
}

func (s *Server) find(repoName string, number int) (*repo, *issue, error) {
	r, err := s.repoFor(repoName)
	if err != nil {
		return nil, nil, err
	}
	is, err := r.issue(number)
	return r, is, err
}

// AddLabels adds labels as by. Only labels the issue lacked produce a
// "labeled" webhook, as on GitHub.
func (s *Server) AddLabels(repoName string, number int, by User, names ...string) error {
	return s.change(func() ([]pending, error) {
		r, is, err := s.find(repoName, number)
		if err != nil {
			return nil, err
		}
		var events []pending
		for _, name := range names {
			if is.hasLabel(name) {
				continue
			}
			is.labels, is.updated = append(is.labels, name), s.now()
			events = append(events, s.event(r, "issues", "labeled", by, map[string]any{"issue": issueJSON(is), "label": jLabel{Name: name}}))
		}
		return events, nil
	})
}

// RemoveLabel removes a label as by; removing one the issue lacks is NOT_FOUND.
func (s *Server) RemoveLabel(repoName string, number int, by User, name string) error {
	return s.change(func() ([]pending, error) {
		r, is, err := s.find(repoName, number)
		if err != nil {
			return nil, err
		}
		if !is.hasLabel(name) {
			return nil, fmt.Errorf("%w: label %q", errNotFound, name)
		}
		is.labels = slices.DeleteFunc(is.labels, func(l string) bool { return l == name })
		is.updated = s.now()
		return []pending{s.event(r, "issues", "unlabeled", by, map[string]any{"issue": issueJSON(is), "label": jLabel{Name: name}})}, nil
	})
}

// AddComment comments on an issue or pull request as by and returns the id.
func (s *Server) AddComment(repoName string, number int, by User, body string) (int64, error) {
	var id int64
	err := s.change(func() ([]pending, error) {
		r, is, err := s.find(repoName, number)
		if err != nil {
			return nil, err
		}
		at := s.now()
		s.nextCommentID++
		c := &comment{id: s.nextCommentID, issue: number, body: body, author: by, created: at, updated: at}
		r.comments = append(r.comments, c)
		is.updated, id = at, c.id
		return []pending{s.event(r, "issue_comment", "created", by, map[string]any{"issue": issueJSON(is), "comment": commentJSON(c)})}, nil
	})
	return id, err
}

// EditComment changes a comment's body as by.
func (s *Server) EditComment(repoName string, id int64, by User, body string) error {
	return s.change(func() ([]pending, error) {
		r, err := s.repoFor(repoName)
		if err != nil {
			return nil, err
		}
		for _, c := range r.comments {
			if c.id != id {
				continue
			}
			is := r.issues[c.issue]
			c.body, c.updated = body, s.now()
			is.updated = c.updated
			return []pending{s.event(r, "issue_comment", "edited", by, map[string]any{"issue": issueJSON(is), "comment": commentJSON(c)})}, nil
		}
		return nil, fmt.Errorf("%w: comment %d", errNotFound, id)
	})
}
