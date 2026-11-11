//go:build smoke

package smoke

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // the journal is read from the install's SQLite file

	"github.com/bestagentkits/sisf/internal/install"
)

// How the run reads the factory, and why:
//   - The current record, run and pull requests come from the REST surface
//     through `sisf call runs.inspect`, with the token the operator's
//     `sisf login` stored under SISF_HOME (never on a command line or in
//     the environment). This is what an Owner sees.
//   - The states visited come from the factory's journal, the `transitions`
//     table of the install's SQLite store, opened read-only. Polling the
//     surface would miss a state the issue passes through between two polls;
//     the journal records every transition with its time.
//   - The usage ledger comes from `sisf call budget.get`.
type factoryObserver struct {
	r         *commandRunner
	sisf      string
	home      string   // the install, where `sisf login` stores the login
	env       []string // sisf environment with SISF_HOME and SISF_URL
	stateDB   string
	factoryID string
	// maxAttempts is the manifest's limit of coding attempts per run.
	maxAttempts int
	// signIn signs the operator in through the browser; keepSignedIn calls
	// it again before the stored login lapses.
	signIn func(context.Context)
	// renewForced makes the next call sign in again whatever the login.
	renewForced bool
}

// inspectView is the part of runs.inspect the run reads.
type inspectView struct {
	Output struct {
		Record struct {
			State           string `json:"state"`
			RunID           string `json:"run_id"`
			CandidateDigest string `json:"candidate_digest"`
			ReleaseDigest   string `json:"release_digest"`
		} `json:"record"`
		Run *struct {
			RunID        string   `json:"run_id"`
			Status       string   `json:"status"`
			PullRequests []string `json:"pull_requests"`
		} `json:"run"`
	} `json:"output"`
}

// budgetView is the part of budget.get the run reads.
type budgetView struct {
	Output struct {
		Confirmed             bool     `json:"confirmed"`
		Mode                  string   `json:"mode"`
		ReservedMicros        int64    `json:"reserved_micros"`
		SpentVerifiedMicros   int64    `json:"spent_verified_micros"`
		SpentUnverifiedMicros int64    `json:"spent_unverified_micros"`
		AvailableMicros       *int64   `json:"available_micros"`
		Warnings              []string `json:"warnings"`
	} `json:"output"`
}

func (b budgetView) spent() int64 {
	return b.Output.SpentVerifiedMicros + b.Output.SpentUnverifiedMicros
}

func (b budgetView) summary() string {
	available := "n/a"
	if b.Output.AvailableMicros != nil {
		available = fmt.Sprint(*b.Output.AvailableMicros)
	}
	return fmt.Sprintf("mode %s, spent verified %d micros, spent unverified %d micros, reserved %d micros, available %s micros, warnings %q",
		b.Output.Mode, b.Output.SpentVerifiedMicros, b.Output.SpentUnverifiedMicros, b.Output.ReservedMicros, available, b.Output.Warnings)
}

// call runs one action of the factory and decodes its answer into out.
func (o *factoryObserver) call(ctx context.Context, action string, input any, out any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	o.keepSignedIn(ctx)
	stdout, err := o.r.run(ctx, time.Minute, o.env, o.sisf, "call", action, "--input", string(raw))
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		return fmt.Errorf("%s answered something that is not the expected JSON: %w", action, err)
	}
	return nil
}

func (o *factoryObserver) inspect(ctx context.Context, issue string) (inspectView, error) {
	var v inspectView
	return v, o.call(ctx, "runs.inspect", map[string]string{"issue": issue}, &v)
}

func (o *factoryObserver) budget(ctx context.Context) (budgetView, error) {
	var v budgetView
	return v, o.call(ctx, "budget.get", map[string]any{}, &v)
}

// approvals lists the approval requests, for the operator to decide.
func (o *factoryObserver) approvals(ctx context.Context) (string, error) {
	o.keepSignedIn(ctx)
	return o.r.run(ctx, time.Minute, o.env, o.sisf, "call", "approvals.list", "--input", `{}`)
}

// journal returns the transitions of issue in order, read-only.
func (o *factoryObserver) journal(ctx context.Context, issue string) ([]stateVisit, error) {
	db, err := sql.Open("sqlite", "file:"+o.stateDB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT to_state, run_id, created_at FROM transitions
		WHERE factory_id = ? AND issue = ? ORDER BY version`, o.factoryID, issue)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []stateVisit
	for rows.Next() {
		var v stateVisit
		var at int64
		if err := rows.Scan(&v.State, &v.RunID, &at); err != nil {
			return nil, err
		}
		v.At = time.Unix(0, at)
		out = append(out, v)
	}
	return out, rows.Err()
}

// newFactoryObserver reads what the observer needs from the install.
func newFactoryObserver(r *commandRunner, sisf, home string) (*factoryObserver, error) {
	l := install.Layout{Dir: home}
	m, err := l.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("the install's factory.yaml cannot be read: %w", err)
	}
	o := &factoryObserver{
		r: r, sisf: sisf, home: home, stateDB: l.StateDB(), factoryID: string(m.FactoryID), maxAttempts: m.Limits.MaxAttempts,
		env: sisfEnv("SISF_HOME="+home, "SISF_URL="+loopbackURL(install.PortControl, "")),
	}
	o.signIn = func(ctx context.Context) { signInOperator(ctx, r, sisf, home, o.env) }
	return o, nil
}
