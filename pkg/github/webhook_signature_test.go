package github_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func TestSignedDeliveryIsAcceptedAndNormalized(t *testing.T) {
	t.Parallel()
	in := &inbox{}
	r := newReceiver(t, in)
	rec := post(t, r, signed(payload(t, "labeled", nil), "issues", "d-1"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	events := in.got()
	if len(events) != 1 {
		t.Fatalf("sink received %d events", len(events))
	}
	ev := events[0]
	if ev.DeliveryID != "d-1" || ev.Type != "issues" || ev.Action != "labeled" || ev.InstallationID != testInstall ||
		ev.Repo != repoRef || ev.Sender.Login != "alice" || ev.Sender.Bot || ev.FromSelf || ev.ReceivedAt.IsZero() {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Issue == nil || ev.Issue.Ref != repoRef.Issue(5) || !ev.Issue.HasLabel(github.LabelReady) || ev.Label == nil || ev.Label.Name != github.LabelReady {
		t.Fatalf("issue = %+v label = %+v", ev.Issue, ev.Label)
	}
	if err := ev.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveriesWithoutAValidSignatureAreRejectedBeforeAnythingIsParsed(t *testing.T) {
	t.Parallel()
	body := payload(t, "labeled", nil)
	good := github.Sign(core.NewSecretValue([]byte(testSecret)), body)
	emptyKey := github.Sign(core.NewSecretValue(nil), body)
	cases := map[string]string{
		"missing":             "",
		"wrong secret":        github.Sign(core.NewSecretValue([]byte("another-secret")), body),
		"signed with no key":  emptyKey,
		"wrong algorithm":     strings.Replace(good, "sha256=", "sha1=", 1),
		"no prefix":           strings.TrimPrefix(good, "sha256="),
		"not hex":             "sha256=" + strings.Repeat("zz", 32),
		"truncated":           good[:len(good)-2],
		"uppercase prefix":    strings.Replace(good, "sha256=", "SHA256=", 1),
		"signature of nobody": "sha256=" + strings.Repeat("00", 32),
	}
	for name, sig := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := &inbox{}
			r := newReceiver(t, in)
			d := signed(body, "issues", "d-1")
			d.signature = sig
			if got := status(t, r, d); got != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", got)
			}
			if len(in.got()) != 0 {
				t.Fatal("an unauthenticated delivery reached the sink")
			}
		})
	}
}

func TestSignatureCoversTheExactBody(t *testing.T) {
	t.Parallel()
	in := &inbox{}
	r := newReceiver(t, in)
	d := signed(payload(t, "labeled", nil), "issues", "d-1")
	d.body = append(d.body, ' ')
	if got := status(t, r, d); got != http.StatusUnauthorized {
		t.Fatalf("a body changed after signing: status %d, want 401", got)
	}
	if len(in.got()) != 0 {
		t.Fatal("a tampered delivery reached the sink")
	}
}

func TestRotatingTheSecretLosesNoDeliveries(t *testing.T) {
	t.Parallel()
	body := payload(t, "labeled", nil)
	oldSig := github.Sign(core.NewSecretValue([]byte("old-secret")), body)
	newSig := github.Sign(core.NewSecretValue([]byte("new-secret")), body)
	active := []string{"old-secret"}
	in := &inbox{}
	r := newReceiver(t, in, func(c *github.ReceiverConfig) {
		c.Secrets = func() []core.SecretValue { return secrets(active...)() }
	})
	send := func(sig, id string) int {
		d := signed(body, "issues", id)
		d.signature = sig
		return status(t, r, d)
	}
	if got := send(newSig, "d-0"); got != http.StatusUnauthorized {
		t.Fatalf("a secret that is not yet active: %d, want 401", got)
	}
	active = []string{"old-secret", "new-secret"}
	if send(oldSig, "d-1") != http.StatusAccepted || send(newSig, "d-2") != http.StatusAccepted {
		t.Fatal("during rotation, deliveries signed with either secret must be accepted")
	}
	active = []string{"new-secret"}
	if got := send(oldSig, "d-3"); got != http.StatusUnauthorized {
		t.Fatalf("a retired secret: %d, want 401", got)
	}
}

func TestEmptySecretsNeverVerifyAnything(t *testing.T) {
	t.Parallel()
	body := payload(t, "labeled", nil)
	for name, source := range map[string]func() []core.SecretValue{
		"no secrets":   func() []core.SecretValue { return nil },
		"empty secret": func() []core.SecretValue { return []core.SecretValue{core.NewSecretValue(nil), {}} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := &inbox{}
			r := newReceiver(t, in, func(c *github.ReceiverConfig) { c.Secrets = source })
			d := signed(body, "issues", "d-1")
			d.signature = github.Sign(core.NewSecretValue(nil), body)
			if got := status(t, r, d); got != http.StatusUnauthorized || len(in.got()) != 0 {
				t.Fatalf("status %d with %d events; an empty key can be forged by anyone", got, len(in.got()))
			}
		})
	}
}

func TestReceiverRefusesToBeBuiltWithoutWhatItsChecksNeed(t *testing.T) {
	t.Parallel()
	ok := github.ReceiverConfig{Secrets: secrets(testSecret), InstallationID: testInstall, Repos: []github.RepoRef{repoRef}, Self: self}
	sink := (&inbox{}).sink
	cases := map[string]func(*github.ReceiverConfig){
		"no secret source": func(c *github.ReceiverConfig) { c.Secrets = nil },
		"no installation":  func(c *github.ReceiverConfig) { c.InstallationID = 0 },
		"no repositories":  func(c *github.ReceiverConfig) { c.Repos = nil },
		"blank repository": func(c *github.ReceiverConfig) { c.Repos = []github.RepoRef{{Owner: "acme"}} },
		"no own account":   func(c *github.ReceiverConfig) { c.Self = github.Identity{} },
	}
	for name, mutate := range cases {
		cfg := ok
		mutate(&cfg)
		if _, err := github.NewReceiver(cfg, sink); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: err = %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if _, err := github.NewReceiver(ok, nil); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no sink: err = %v", err)
	}
}
