package core

import (
	"slices"
	"strings"
	"testing"
)

const (
	testSHA    = CommitSHA("0123456789abcdef0123456789abcdef01234567")
	testDigest = Digest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
)

func TestIssueRefRoundTripsThroughItsTextForm(t *testing.T) {
	ref := IssueRef{Owner: "bestagentkits", Repo: "sisf-sandbox", Number: 42}
	if got := ref.String(); got != "bestagentkits/sisf-sandbox#42" {
		t.Fatalf("String() = %q", got)
	}
	parsed, err := ParseIssueRef(ref.String())
	if err != nil || parsed != ref {
		t.Fatalf("ParseIssueRef = %+v, %v", parsed, err)
	}
}

func TestMalformedIssueRefsAreRejected(t *testing.T) {
	for _, text := range []string{
		"", "sisf#1", "owner/repo", "owner/repo#", "owner/repo#0", "owner/repo#-3",
		"owner/repo#1x", "/repo#1", "owner/#1", "ow ner/repo#1", "owner/re/po#1",
	} {
		if _, err := ParseIssueRef(text); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("ParseIssueRef(%q) error code = %q, want %q", text, CodeOf(err), CodeInvalidArgument)
		}
	}
	if !(IssueRef{}).IsZero() || (IssueRef{}).Validate() == nil {
		t.Error("the zero issue ref must be zero and invalid")
	}
}

func TestIdentifiersRejectEmptyAndUnsafeText(t *testing.T) {
	for _, good := range []string{"acme-factory", "f1", "dev.local_2"} {
		if err := FactoryID(good).Validate(); err != nil {
			t.Errorf("FactoryID(%q): %v", good, err)
		}
		if err := DeploymentID(good).Validate(); err != nil {
			t.Errorf("DeploymentID(%q): %v", good, err)
		}
		if err := RunID(good).Validate(); err != nil {
			t.Errorf("RunID(%q): %v", good, err)
		}
	}
	for _, bad := range []string{"", "Has Space", "UPPER", "-lead", "a/b", "a:b", strings.Repeat("a", 64)} {
		if FactoryID(bad).Validate() == nil || DeploymentID(bad).Validate() == nil || RunID(bad).Validate() == nil {
			t.Errorf("identifier %q must be rejected", bad)
		}
	}
}

func TestDigestsMustNameAnAlgorithmAndCarryFullLengthHex(t *testing.T) {
	sha512 := Digest("sha512:" + strings.Repeat("ab", 64))
	for _, good := range []Digest{testDigest, sha512} {
		if err := good.Validate(); err != nil {
			t.Errorf("Digest(%q): %v", good, err)
		}
	}
	for _, bad := range []Digest{
		"", "sha256:", "sha256:abc", Digest(strings.TrimPrefix(string(testDigest), "sha256:")),
		Digest(strings.ToUpper(string(testDigest))), "md5:0123456789abcdef0123456789abcdef",
		Digest("sha512:" + strings.Repeat("ab", 32)),
	} {
		if bad.Validate() == nil {
			t.Errorf("Digest(%q) must be rejected", bad)
		}
	}
	if err := ReleaseDigest(testDigest).Validate(); err != nil {
		t.Errorf("ReleaseDigest: %v", err)
	}
	if ReleaseDigest("latest").Validate() == nil {
		t.Error("a mutable tag must not pass as a release digest")
	}
}

func TestCommitSHAsMustBeFullLengthLowercaseHex(t *testing.T) {
	for _, good := range []CommitSHA{testSHA, CommitSHA(strings.Repeat("a", 64))} {
		if err := good.Validate(); err != nil {
			t.Errorf("CommitSHA(%q): %v", good, err)
		}
	}
	for _, bad := range []CommitSHA{"", "main", "0123456", CommitSHA(strings.ToUpper(string(testSHA))), CommitSHA(strings.Repeat("a", 41))} {
		if bad.Validate() == nil {
			t.Errorf("CommitSHA(%q) must be rejected", bad)
		}
	}
}

func TestAFactoryHasExactlyThreeZones(t *testing.T) {
	want := []Zone{ZoneApplication, ZoneAgent, ZoneControl}
	if got := Zones(); !slices.Equal(got, want) {
		t.Fatalf("Zones() = %v, want %v", got, want)
	}
	for i, z := range want {
		if z.Number() != i+1 || !z.Valid() || z.Shared() {
			t.Errorf("zone %q: number %d valid %v shared %v", z, z.Number(), z.Valid(), z.Shared())
		}
	}
	if !ZoneShared.Valid() || !ZoneShared.Shared() || ZoneShared.Number() != 0 {
		t.Error("shared placement must be valid, shared and unnumbered")
	}
	if slices.Contains(Zones(), ZoneShared) {
		t.Error("shared placement is not a zone")
	}
	if Zone("zone4").Valid() || Zone("").Valid() {
		t.Error("unknown zones must be rejected")
	}
}
