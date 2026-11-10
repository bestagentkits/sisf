package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestUndeclaredCapabilitiesAreNotSupported(t *testing.T) {
	set := CapabilitySet{
		CapAgentCancel:     SupportSupported,
		CapAgentUsage:      SupportUnverified,
		CapAgentCheckpoint: SupportNotSupported,
	}
	if set.Level(CapAgentResume) != SupportNotSupported || (CapabilitySet(nil)).Level(CapRollback) != SupportNotSupported {
		t.Error("a capability that was never declared is NOT_SUPPORTED")
	}
	if !set.Supports(CapAgentCancel) {
		t.Error("a supported capability must be usable")
	}
	if set.Supports(CapAgentUsage) || set.Supports(CapAgentCheckpoint) || set.Supports(CapAgentResume) {
		t.Error("unverified, unsupported and undeclared capabilities must not be relied on")
	}
	if (CapabilitySet{CapRollback: "yes"}).Supports(CapRollback) {
		t.Error("an unknown support level must not count as supported")
	}
}

func TestSupportLevelsKeepTheirContractSpelling(t *testing.T) {
	if SupportNotSupported != "NOT_SUPPORTED" || SupportUnverified != "unverified" || SupportSupported != "supported" {
		t.Fatal("support level spelling changed")
	}
	for _, l := range []SupportLevel{SupportSupported, SupportUnverified, SupportNotSupported} {
		if !l.Valid() {
			t.Errorf("support level %q must be valid", l)
		}
	}
	if SupportLevel("").Valid() || SupportLevel("partial").Valid() {
		t.Error("unknown support levels must be rejected")
	}
}

func TestFullDeploymentRequiresTheFiveProfileCapabilities(t *testing.T) {
	want := []Capability{CapSingleFactoryDeployment, CapDurableTasks, CapSafeExec, CapReleasableZone1, CapRollback}
	got := ProfileCapabilities()
	if len(got) != len(want) {
		t.Fatalf("ProfileCapabilities() = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ProfileCapabilities()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSecretValueNeverPrintsItsContent(t *testing.T) {
	const plain = "ghs_super-secret-token"
	secret := NewSecretValue([]byte(plain))
	type holder struct {
		Exported SecretValue
		hidden   SecretValue
	}
	h := holder{Exported: secret, hidden: secret}

	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("token issued", "token", secret, "holder", h)
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	text, err := secret.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{
		secret.String(), fmt.Sprint(secret), fmt.Sprintf("%v %+v %#v %s %q %x %d", secret, secret, secret, secret, secret, secret, secret),
		fmt.Sprintf("%v %+v %#v", h, h, h), fmt.Sprintf("%v %+v %#v", &h, &secret, []SecretValue{secret}),
		string(raw), string(text), logged.String(),
	}
	hexForm := fmt.Sprintf("%x", plain)
	for _, out := range outputs {
		if strings.Contains(out, plain) || strings.Contains(out, hexForm) {
			t.Errorf("secret leaked in %q", out)
		}
	}
	if !strings.Contains(secret.String(), "REDACTED") {
		t.Errorf("String() = %q", secret.String())
	}
}

func TestSecretValueIsRevealedOnlyOnRequestAndIsolatedFromCallers(t *testing.T) {
	source := []byte("s3cret")
	secret := NewSecretValue(source)
	source[0] = 'X'
	revealed := secret.Reveal()
	if string(revealed) != "s3cret" {
		t.Fatalf("Reveal() = %q", revealed)
	}
	revealed[0] = 'Y'
	if string(secret.Reveal()) != "s3cret" {
		t.Error("a caller changed the stored secret through a revealed copy")
	}
	if secret.IsZero() || !(SecretValue{}).IsZero() || (SecretValue{}).Reveal() != nil {
		t.Error("only the zero value is empty")
	}
}

func TestEgressPolicyValidation(t *testing.T) {
	good := EgressPolicy{Mode: EgressAllowlist, Allow: []string{"github.com", "*.githubusercontent.com", "proxy.golang.org"}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	if err := (EgressPolicy{Mode: EgressUnrestricted, Allow: good.Allow}).Validate(); err != nil {
		t.Fatalf("turning blocking off is an Owner choice and must stay expressible: %v", err)
	}
	for name, p := range map[string]EgressPolicy{
		"no mode":               {},
		"unknown mode":          {Mode: "open"},
		"url instead of domain": {Mode: EgressAllowlist, Allow: []string{"https://github.com/"}},
		"bare wildcard":         {Mode: EgressAllowlist, Allow: []string{"*"}},
		"inner wildcard":        {Mode: EgressAllowlist, Allow: []string{"api.*.com"}},
		"uppercase domain":      {Mode: EgressAllowlist, Allow: []string{"GitHub.com"}},
		"empty entry":           {Mode: EgressAllowlist, Allow: []string{""}},
	} {
		if CodeOf(p.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, CodeInvalidArgument, p.Validate())
		}
	}
}

func TestAuditOutcomesAreAClosedSet(t *testing.T) {
	for _, o := range []AuditOutcome{OutcomeAllowed, OutcomeDenied, OutcomeExecuted, OutcomeFailed} {
		if !o.Valid() {
			t.Errorf("outcome %q must be valid", o)
		}
	}
	if AuditOutcome("").Valid() || AuditOutcome("edited").Valid() {
		t.Error("unknown outcomes must be rejected")
	}
}

// chainStub and keyStub prove the audit and signing contracts can be met with exported types alone.
type chainStub struct{ events []AuditEvent }

func (c *chainStub) Append(_ context.Context, e AuditEvent) (AuditEvent, error) {
	e.Seq = int64(len(c.events) + 1)
	c.events = append(c.events, e)
	return e, nil
}
func (c *chainStub) Verify(context.Context) error { return nil }
func (c *chainStub) Events(_ context.Context, afterSeq int64, _ int) ([]AuditEvent, error) {
	return c.events[afterSeq:], nil
}

type keyStub struct{}

func (keyStub) KeyID() string { return "k1" }
func (keyStub) Sign(context.Context, []byte) (Signature, error) {
	return Signature{KeyID: "k1", Algorithm: "ed25519", Value: []byte{1}}, nil
}
func (keyStub) Verify(context.Context, []byte, Signature) error {
	return NewError(CodeSignatureInvalid, "unknown key")
}

func TestAuditSigningAndClockContractsAreImplementable(t *testing.T) {
	stub := &chainStub{}
	var (
		log      AuditLog    = stub
		reader   AuditReader = stub
		signer   Signer      = keyStub{}
		verifier Verifier    = keyStub{}
		clock    Clock       = SystemClock{}
	)
	ctx := context.Background()
	written, err := log.Append(ctx, AuditEvent{Action: "approvals.decide", Outcome: OutcomeDenied, At: clock.Now()})
	if err != nil || written.Seq != 1 {
		t.Fatalf("Append = %+v, %v", written, err)
	}
	if events, err := reader.Events(ctx, 0, 10); err != nil || len(events) != 1 {
		t.Fatalf("Events = %v, %v", events, err)
	}
	sig, err := signer.Sign(ctx, []byte("payload"))
	if err != nil || sig.KeyID != signer.KeyID() {
		t.Fatalf("Sign = %+v, %v", sig, err)
	}
	if CodeOf(verifier.Verify(ctx, []byte("payload"), sig)) != CodeSignatureInvalid {
		t.Error("a failed verification must be typed SIGNATURE_INVALID")
	}
	if log.Verify(ctx) != nil {
		t.Error("an untouched chain verifies")
	}
	if !CodeTampered.Valid() {
		t.Error("a broken chain needs its own error code")
	}
}

func TestSystemClockReportsUTCAndFiresTimers(t *testing.T) {
	clock := SystemClock{}
	before := time.Now()
	now := clock.Now()
	if now.Location() != time.UTC {
		t.Errorf("Now() location = %v, want UTC", now.Location())
	}
	if now.Before(before.Add(-time.Second)) || now.After(before.Add(time.Minute)) {
		t.Errorf("Now() = %v is not the current instant", now)
	}
	select {
	case <-clock.After(time.Millisecond):
	case <-time.After(5 * time.Second):
		t.Fatal("After never fired")
	}
}
