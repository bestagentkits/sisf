package signing

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestKeyIDIsAStableFingerprintOfThePublicKey(t *testing.T) {
	t.Parallel()
	first, _ := newKey(t)
	second, _ := newKey(t)
	again := slices.Clone(first)
	if KeyID(first) != KeyID(again) || KeyID(first) == KeyID(second) {
		t.Error("a key id must identify exactly one public key")
	}
	if !strings.HasPrefix(KeyID(first), "ed25519:") || len(KeyID(first)) != len("ed25519:")+64 {
		t.Errorf("KeyID = %q, want ed25519:<64 hex>", KeyID(first))
	}
}

func TestPublicKeyRoundTripsAsOneLineAndAsPEM(t *testing.T) {
	t.Parallel()
	pub, _ := newKey(t)

	line := EncodePublicKey(pub)
	if strings.ContainsAny(line, "\n ") {
		t.Errorf("encoded public key %q must fit one line, as an environment variable needs", line)
	}
	parsed, err := ParsePublicKey("  " + line + "\n")
	if err != nil || !parsed.Equal(pub) {
		t.Fatalf("ParsePublicKey(one line) = %v, %v", parsed, err)
	}

	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err = ParsePublicKey(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	if err != nil || !parsed.Equal(pub) {
		t.Fatalf("ParsePublicKey(PEM) = %v, %v", parsed, err)
	}
}

func TestMalformedPublicKeyIsRejectedWithoutEchoingIt(t *testing.T) {
	t.Parallel()
	_, priv := newKey(t)
	privatePEM, err := EncodePrivateKey(priv)
	if err != nil {
		t.Fatalf("EncodePrivateKey: %v", err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}
	ecDER, err := x509.MarshalPKIXPublicKey(&ecKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cases := map[string]string{
		"empty":                    "",
		"not base64":               "not a key!",
		"too short":                base64.StdEncoding.EncodeToString([]byte("short")),
		"private key bytes":        base64.StdEncoding.EncodeToString(priv),
		"private key PEM":          string(privatePEM),
		"public key of other type": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: ecDER})),
		"PEM with garbage inside":  "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ParsePublicKey(text)
			wantCode(t, err, core.CodeInvalidArgument)
			if text != "" && strings.Contains(err.Error(), strings.TrimSpace(text)) {
				t.Errorf("the error repeats the rejected key material: %v", err)
			}
		})
	}
}

func TestPrivateKeyRoundTripsThroughPEM(t *testing.T) {
	t.Parallel()
	_, priv := newKey(t)
	encoded, err := EncodePrivateKey(priv)
	if err != nil {
		t.Fatalf("EncodePrivateKey: %v", err)
	}
	parsed, err := ParsePrivateKey(encoded)
	if err != nil || !parsed.Equal(priv) {
		t.Fatalf("ParsePrivateKey = %v", err)
	}
	for name, bad := range map[string][]byte{
		"empty":      nil,
		"not PEM":    []byte("hello"),
		"public PEM": []byte("-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n"),
		"truncated":  bytes.Replace(encoded, []byte("-----END PRIVATE KEY-----"), nil, 1),
	} {
		if _, err := ParsePrivateKey(bad); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: ParsePrivateKey = %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if _, err := EncodePrivateKey(priv[:8]); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a truncated private key was encoded: %v", err)
	}
}

func TestRootPublicKeyIsResolvedFromTheEnvironment(t *testing.T) {
	t.Parallel()
	pub, _ := newKey(t)
	env := func(name string) (string, bool) {
		if name == "SISF_ROOT_PUBLIC_KEY" {
			return EncodePublicKey(pub) + "\n", true
		}
		return "", false
	}
	got, err := ResolvePublicKey("env:SISF_ROOT_PUBLIC_KEY", env)
	if err != nil || !got.Equal(pub) {
		t.Fatalf("ResolvePublicKey = %v, %v", got, err)
	}
}

func TestMissingRootPublicKeyIsNeverReplacedByADefault(t *testing.T) {
	t.Parallel()
	pub, _ := newKey(t)
	inline := EncodePublicKey(pub)
	env := func(name string) (string, bool) {
		switch name {
		case "BLANK":
			return "  ", true
		case "GARBAGE":
			return "not a key", true
		}
		return "", false
	}
	cases := map[string]struct {
		ref  string
		code core.ErrorCode
	}{
		"no reference":                    {"", core.CodeNotFound},
		"blank reference":                 {"  ", core.CodeNotFound},
		"variable not set":                {"env:SISF_ROOT_PUBLIC_KEY", core.CodeNotFound},
		"variable blank":                  {"env:BLANK", core.CodeNotFound},
		"variable holds no key":           {"env:GARBAGE", core.CodeInvalidArgument},
		"store that cannot be read here":  {"local:keys/root.pub", core.CodeNotSupported},
		"source outside the contract":     {"vault:root", core.CodeInvalidArgument},
		"file path instead of reference":  {"file:/etc/sisf/root.pub", core.CodeInvalidArgument},
		"source without a name":           {"env:", core.CodeInvalidArgument},
		"key written inline":              {inline, core.CodeInvalidArgument},
		"variable name that is not valid": {"env:root key", core.CodeInvalidArgument},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			key, err := ResolvePublicKey(tc.ref, env)
			wantCode(t, err, tc.code)
			if key != nil {
				t.Errorf("a key was returned together with an error")
			}
			if strings.Contains(err.Error(), inline) {
				t.Errorf("the error repeats a value that was given in place of a reference: %v", err)
			}
		})
	}
}
