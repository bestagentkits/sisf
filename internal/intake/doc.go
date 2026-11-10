// Package intake takes a GitHub issue from the label that asks for work to
// a scope an agent may work from, or to the approval inbox.
//
// The label sisf:ready only asks for intake. What happens next is decided
// from the issue as GitHub shows it, the policy the Owner approved and the
// approval store, never from what the issue or a comment says: that text is
// quoted, compared and hashed, and nothing in it is obeyed.
//
// A request that lacks information is asked for it once per answer and at
// most a few times; without a complete answer in time the issue is parked
// as sisf:needs-info and costs nothing more until a person lifts it. A
// complete request becomes a numbered Scope of Work with a digest. A scope
// inside what the Owner delegated goes to Ready; a new feature, a change of
// the rules and every request from someone without write access wait for
// the Owner. An edit that changes the digest makes a new version, and an
// approval of the old one does not cover it.
//
// Every lifecycle step is put before the lifecycle machine, which judges
// the proof of the guarded steps itself, and is written in one transaction
// with the documents of intake and the effects to show on GitHub. Those
// effects leave through the outbox and are performed by Deliver.
package intake
