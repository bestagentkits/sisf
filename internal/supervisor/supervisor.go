package supervisor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
)

// shutdownGrace is how long in-flight status requests get to finish when the
// supervisor stops.
const shutdownGrace = 5 * time.Second

// Options are the supervisor's dependencies. A zero value uses the real
// clock, the process environment and the running executable.
type Options struct {
	Clock core.Clock
	// LookupEnv reads the environment for an env: root key reference.
	LookupEnv func(string) (string, bool)
	// Measure returns the digest of the running supervisor.
	Measure Measure
	// EmergencyStop, when set, is served on EmergencyStopPath of the admin
	// socket. Without it the socket answers status only.
	EmergencyStop EmergencyStopper
	// FreezeStatus, when set, answers GET on EmergencyStopPath.
	FreezeStatus FreezeStatusReader
	// Rollback, when set, is served on RollbackPath: an Owner-approved
	// rollback to the last known good release.
	Rollback Rollbacker
}

// Supervisor is a supervisor that passed its startup checks. There is no
// other way to obtain one, so holding a Supervisor means the checks passed.
type Supervisor struct {
	status   Status
	addr     adminAddr
	listener net.Listener
	server   *http.Server
}

// Start runs the startup checks and, only when all pass, opens the admin
// socket. It refuses with an error when:
//
//   - the root public key is missing, or its reference cannot be resolved;
//   - the release manifest is missing, unsigned, edited, signed by another
//     key or signed for another factory;
//   - the digest of the running supervisor differs from the pinned one;
//   - the trust volume is missing or writable by other accounts, or the LKG
//     record is configured outside it or does not verify;
//   - the admin address is not local.
//
// A refused supervisor has opened nothing. Call Serve to answer status
// requests and Close to release the socket.
func Start(ctx context.Context, cfg Config, opts Options) (*Supervisor, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	addr, err := parseAdminAddr(cfg.AdminAddr)
	if err != nil {
		return nil, err
	}
	clock, lookupEnv, measure := opts.Clock, opts.LookupEnv, opts.Measure
	if clock == nil {
		clock = core.SystemClock{}
	}
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if measure == nil {
		measure = MeasureExecutable
	}

	rootKey, err := signing.ResolvePublicKey(cfg.RootKeyRef, lookupEnv)
	if err != nil {
		return nil, err
	}
	verifier, err := signing.NewVerifier(rootKey)
	if err != nil {
		return nil, err
	}
	check, err := checkSelf(ctx, cfg, verifier, measure)
	if err != nil {
		return nil, err
	}
	lkg, err := loadLKGStatus(ctx, cfg, verifier)
	if err != nil {
		return nil, err
	}

	listener, err := listenAdmin(addr)
	if err != nil {
		return nil, err
	}
	if addr.network == "tcp" {
		// Report the port actually bound, which differs when port 0 was asked for.
		addr.address = listener.Addr().String()
	}
	sup := &Supervisor{
		status: Status{
			FactoryID: cfg.FactoryID,
			StartedAt: clock.Now(),
			SelfCheck: SelfCheckStatus{
				Passed:           true,
				ExecutableDigest: check.measured,
				ManifestKeyID:    check.release.Signature.KeyID,
				ManifestIssuedAt: check.release.Manifest.IssuedAt,
			},
			LKG: lkg,
		},
		addr:     addr,
		listener: listener,
	}
	sup.server = &http.Server{
		Handler:           newAdminMux(sup.Status, opts),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	return sup, nil
}

// loadLKGStatus opens the LKG store on the trust volume and reads the
// record, if any. No record is a valid state: nothing has been released yet,
// and a later release step holds until one exists.
func loadLKGStatus(ctx context.Context, cfg Config, verifier core.Verifier) (LKGStatus, error) {
	store, err := OpenLKGStore(cfg.TrustDir, cfg.LKGPath, cfg.FactoryID, verifier)
	if err != nil {
		return LKGStatus{}, err
	}
	record, found, err := store.Load(ctx)
	if err != nil || !found {
		return LKGStatus{}, err
	}
	return LKGStatus{Recorded: true, RecordedAt: record.RecordedAt, Units: record.Release.Manifest.Units}, nil
}

// Status returns what the supervisor reports about itself.
func (s *Supervisor) Status() Status { return s.status }

// AdminAddr returns the address the admin socket is bound to, in the form
// FetchStatus accepts.
func (s *Supervisor) AdminAddr() string { return s.addr.String() }

// Serve answers status requests until ctx is cancelled, then stops and
// releases the socket. It returns nil after an orderly stop.
func (s *Supervisor) Serve(ctx context.Context) error {
	served := make(chan error, 1)
	go func() { served <- s.server.Serve(s.listener) }()
	select {
	case err := <-served:
		return newError(core.CodeUnavailable, "the admin socket %s stopped serving", s.addr).WithCause(err)
	case <-ctx.Done():
	}
	// The caller's context is already cancelled; the grace period needs its own.
	stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	err := s.server.Shutdown(stopping)
	<-served
	if err != nil {
		return newError(core.CodeTimeout, "the admin socket %s did not stop within %s", s.addr, shutdownGrace).WithCause(err)
	}
	return nil
}

// Close releases the admin socket of a supervisor that is not serving. It is
// safe to call after Serve has returned.
func (s *Supervisor) Close() error {
	if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return newError(core.CodeUnavailable, "the admin socket %s cannot be closed", s.addr).WithCause(err)
	}
	return nil
}
