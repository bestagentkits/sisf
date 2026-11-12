// Package lifecycle is the agent zone's part of the issue lifecycle after
// the coding agent finished: it exports the patch a run's agent left, read
// and checked against its digest, for the supervisor to build and judge;
// it starts the next coding attempt once the supervisor failed the last
// one; and it gives every coding attempt a fresh workspace.
//
// The agent zone holds neither GitHub credentials nor a container runtime,
// so it never commits or builds anything itself. It writes only its own
// documents, in the "dispatcher" namespace the control zone reads (see
// package handoff).
package lifecycle
