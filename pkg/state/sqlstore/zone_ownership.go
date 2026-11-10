package sqlstore

import (
	"fmt"
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Which lifecycle steps and which documents belong to which zone. Table
// privileges say what a zone may do to a table; these rules say which rows
// of the lifecycle and of the documents it may do it to. The store checks
// them on every operation and the PostgreSQL row policies are generated from
// them, so a stolen zone credential used with plain SQL is held to the same
// rules as the zone's code.

// runEvents are the steps the agent zone writes: the dispatcher claims a
// Ready issue under its lease and a budget reservation and starts
// development, the run submits the candidate it built, and the run's QA
// sends a failed candidate back into development for the next attempt,
// within the attempts the run has left.
//
// Every other step is the control zone's. Intake and scoping record the
// conversation about the issue; every way into Ready rests on the Owner's
// approval, a delegation of the policy in force or an authorized retry;
// passing QA rests on a signed receipt the control zone verifies; and the
// rollout, its observation and rollback and the closing of the issue belong
// to the release supervisor. A run that could write any of them could start
// unapproved work or release its own candidate.
var runEvents = []core.Event{
	core.EventClaimed, core.EventDevelopmentStarted, core.EventCandidateSubmitted, core.EventQAFailed,
}

// controlEvents are the steps the control zone writes: all of them, the run
// steps included, since the control plane is where the lifecycle is kept.
// The list is explicit so that a step added to the lifecycle has no writer
// until one is chosen for it.
var controlEvents = []core.Event{
	core.EventIssueReceived, core.EventInfoMissing, core.EventClarificationCompleted, core.EventInfoSufficient,
	core.EventApprovalRequired, core.EventScopePermitted, core.EventOwnerApproved, core.EventApprovalDenied,
	core.EventClaimed, core.EventDevelopmentStarted, core.EventCandidateSubmitted, core.EventQAFailed,
	core.EventQAPassed, core.EventRolloutStarted, core.EventRolloutHealthy, core.EventErrorThresholdExceeded,
	core.EventPostDeployVerified, core.EventRegressionDetected, core.EventIssueClosed, core.EventRecoveryReported,
	core.EventRetryAuthorized,
}

// zoneEvents is the allow-list of lifecycle steps per zone. The application
// zone writes none.
var zoneEvents = map[core.Zone]map[core.Event]bool{
	core.ZoneAgent:   eventSet(runEvents),
	core.ZoneControl: eventSet(controlEvents),
}

func eventSet(events []core.Event) map[core.Event]bool {
	set := make(map[core.Event]bool, len(events))
	for _, event := range events {
		set[event] = true
	}
	return set
}

// kvNamespace says which zone writes the documents of a namespace and which
// other zones may read them.
type kvNamespace struct {
	owner   core.Zone
	readers []core.Zone
}

// namespaceOwner owns every namespace kvNamespaces does not list, and no
// other zone reads those: the approval records, the policy in force and the
// control plane's views among them.
const namespaceOwner = core.ZoneControl

// kvNamespaces lists the namespaces another zone than namespaceOwner owns
// or reads. The names are those the components use: the dispatcher's run
// records, the installer's budget confirmation, the scope intake approved
// and the policy references the control zone publishes for runs. The agent
// zone reads the last three to start a run and writes only the first.
var kvNamespaces = map[string]kvNamespace{
	"dispatcher":     {owner: core.ZoneAgent, readers: []core.Zone{core.ZoneControl}},
	"install":        {owner: core.ZoneControl, readers: []core.Zone{core.ZoneAgent}},
	"intake":         {owner: core.ZoneControl, readers: []core.Zone{core.ZoneAgent}},
	"control.policy": {owner: core.ZoneControl, readers: []core.Zone{core.ZoneAgent}},
}

// namespaceRule returns the rule of a namespace.
func namespaceRule(namespace string) kvNamespace {
	if rule, listed := kvNamespaces[namespace]; listed {
		return rule
	}
	return kvNamespace{owner: namespaceOwner}
}

// mayRead reports whether zone may read the documents of namespace.
func (n kvNamespace) mayRead(zone core.Zone) bool {
	return n.owner == zone || slices.Contains(n.readers, zone)
}

// mayWrite reports whether zone may write the documents of namespace.
func (n kvNamespace) mayWrite(zone core.Zone) bool { return n.owner == zone }

// readsDocuments checks that the transaction's zone may read namespace.
func (t *tx) readsDocuments(namespace string) error {
	if err := t.reads(tableKV); err != nil {
		return err
	}
	if !namespaceRule(namespace).mayRead(t.store.zone) {
		return core.NewError(core.CodePermissionDenied,
			fmt.Sprintf("the %s zone may not read the documents of %q", string(t.store.zone), namespace))
	}
	return nil
}

// writesDocuments checks that the transaction may change the documents of
// namespace with need.
func (t *tx) writesDocuments(namespace string, need privilege) error {
	if err := t.writes(tableKV, need); err != nil {
		return err
	}
	if !namespaceRule(namespace).mayWrite(t.store.zone) {
		return core.NewError(core.CodePermissionDenied,
			fmt.Sprintf("the %s zone may not write the documents of %q", string(t.store.zone), namespace))
	}
	return nil
}

// writesStep checks that the transaction may write a lifecycle step on
// event: the record and its journal, and the step itself.
func (t *tx) writesStep(tr core.Transition) error {
	need := canSelect | canUpdate
	if tr.Creates() {
		need |= canInsert
	}
	if err := t.writes(tableRecords, need); err != nil {
		return err
	}
	if err := t.writes(tableTransitions, canSelect|canInsert); err != nil {
		return err
	}
	if !zoneEvents[t.store.zone][tr.Event] {
		return core.NewError(core.CodePermissionDenied,
			fmt.Sprintf("the %s zone may not write the lifecycle step %q", string(t.store.zone), string(tr.Event)))
	}
	return nil
}
