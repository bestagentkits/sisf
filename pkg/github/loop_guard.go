package github

import "strings"

// Identity is the GitHub account the factory acts as: its app bot. Edits the
// factory makes come back as webhooks sent by this account.
type Identity struct {
	ID    int64
	Login string
}

// IsZero reports whether no account is named.
func (i Identity) IsZero() bool { return i.ID == 0 && i.Login == "" }

// Matches reports whether u is the factory's own account. The numeric ID is
// the strong check; the login is compared without case because GitHub
// treats logins that way.
func (i Identity) Matches(u User) bool {
	if i.ID != 0 && u.ID == i.ID {
		return true
	}
	return i.Login != "" && strings.EqualFold(u.Login, i.Login)
}

// Caused reports whether the event is an echo of the factory's own edit: it
// was sent by the factory's account, or it concerns a comment the factory
// wrote. Such events must not be processed again as new input, or a label
// the factory sets would trigger the factory forever.
//
// Only the account decides. A person who copies the factory's progress
// marker into their own comment is still a person.
func (i Identity) Caused(ev WebhookEvent) bool {
	if i.IsZero() {
		return false
	}
	return i.Matches(ev.Sender) || (ev.Comment != nil && i.Matches(ev.Comment.Author))
}
