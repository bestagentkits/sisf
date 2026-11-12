// Package local is the deployment backend of a Standalone factory: it runs
// each revision as a process on one of two fixed ports, blue and green, and
// the edge sends all traffic to one of them. It switches in one step, so it
// declares blue-green and not canary, and it accepts only 100 percent.
//
// A revision is judged by what runs: the backend's own record of the process
// and the revision the application reports at /healthz must agree, whatever
// an earlier call answered.
package local
