// Package local is the secret provider of a factory's own install: secrets
// encrypted at rest with AES-256-GCM in one file of the zone's secret
// directory, under a key read from a file of the Zone 3 secret directory.
//
// The whole store is one sealed document, so a changed byte anywhere, an
// entry moved between names, or the wrong key all fail to open and nothing
// is returned. Files are written for their owner only, and a key file that
// others can read is refused. No value ever appears in an error.
//
// A store serves one zone: it holds and resolves only references owned by
// that zone. One process writes a store at a time.
package local

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"sync"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

// ProviderName is the provider a reference names to be resolved here.
const ProviderName = "local"

// File modes: owner-only.
const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

// keySize is the AES-256 key length.
const keySize = 32

// fileMagic opens every store file and is bound into the seal, so the
// format cannot be swapped without detection.
var fileMagic = []byte("SISF-SECRETS-V1\n")

// Store is an encrypted secret provider. It implements
// secrets.SecretProvider and secrets.Writer and is safe for concurrent use
// within one process.
type Store struct {
	path string
	zone core.Zone
	key  core.SecretValue
	// random supplies nonces.
	random io.Reader

	mu sync.Mutex
}

// Config locates a store and its key.
type Config struct {
	// Path is the store file. Its directory is created owner-only.
	Path string
	// KeyFile holds the 32-byte key in unpadded URL-safe base64, the form
	// the installer writes its tokens in. It must be readable by its owner
	// only.
	KeyFile string
	// Zone is the zone whose secrets the store holds.
	Zone core.Zone
	// Random supplies nonces; nil means crypto/rand.
	Random io.Reader
}

// Open returns the store at cfg.Path. A store that exists is opened once to
// prove the key fits it: the wrong key, or a file that was changed, fails
// here and no store is returned.
func Open(cfg Config) (*Store, error) {
	if cfg.Path == "" || cfg.KeyFile == "" {
		return nil, invalid("a local secret store needs a file and a key file")
	}
	if cfg.Zone.Number() == 0 {
		return nil, invalid("a local secret store serves exactly one of the three zones")
	}
	key, err := readKey(cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	random := cfg.Random
	if random == nil {
		random = rand.Reader
	}
	s := &Store{path: cfg.Path, zone: cfg.Zone, key: key, random: random}
	if _, err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// entry is one stored secret inside the sealed document.
type entry struct {
	Ref   secrets.SecretRef `json:"ref"`
	Value []byte            `json:"value"`
}

// document is the plaintext of the store, sealed as a whole.
type document struct {
	Entries []entry `json:"entries"`
}

// wipe clears every value in the document.
func (d *document) wipe() {
	for i := range d.Entries {
		clear(d.Entries[i].Value)
	}
}

// Resolve returns the value of ref, NOT_FOUND when the store has none and
// PERMISSION_DENIED for a reference this store may not serve.
func (s *Store) Resolve(_ context.Context, ref secrets.SecretRef) (core.SecretValue, error) {
	if err := s.accepts(ref); err != nil {
		return core.SecretValue{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return core.SecretValue{}, err
	}
	defer doc.wipe()
	for _, e := range doc.Entries {
		if e.Ref == ref {
			return core.NewSecretValue(e.Value), nil
		}
	}
	return core.SecretValue{}, core.NewError(core.CodeNotFound, "secret "+ref.String()+" is not in the store")
}

// ListRefs lists the stored references, sorted by name. It has no way to
// return values.
func (s *Store) ListRefs(_ context.Context) ([]secrets.SecretRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	defer doc.wipe()
	refs := make([]secrets.SecretRef, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		refs = append(refs, e.Ref)
	}
	return refs, nil
}

// Put stores value under ref, replacing a value stored under the same name.
// An empty value is refused.
func (s *Store) Put(_ context.Context, ref secrets.SecretRef, value core.SecretValue) error {
	if err := s.accepts(ref); err != nil {
		return err
	}
	if value.IsZero() {
		return invalid("secret %s cannot be stored empty", ref.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	defer doc.wipe()
	doc.Entries = slices.DeleteFunc(doc.Entries, func(e entry) bool { return e.Ref.Name == ref.Name })
	doc.Entries = append(doc.Entries, entry{Ref: ref, Value: value.Reveal()})
	return s.save(doc)
}

// Delete removes ref. A reference that is not stored is NOT_FOUND.
func (s *Store) Delete(_ context.Context, ref secrets.SecretRef) error {
	if err := s.accepts(ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	defer doc.wipe()
	before := len(doc.Entries)
	doc.Entries = slices.DeleteFunc(doc.Entries, func(e entry) bool { return e.Ref == ref })
	if len(doc.Entries) == before {
		return core.NewError(core.CodeNotFound, "secret "+ref.String()+" is not in the store")
	}
	return s.save(doc)
}

// accepts checks that ref is well formed, names this provider and belongs
// to this store's zone.
func (s *Store) accepts(ref secrets.SecretRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Provider != ProviderName {
		return invalid("secret %s is not a %s reference", ref.String(), ProviderName)
	}
	if ref.Zone != s.zone {
		return core.NewError(core.CodePermissionDenied, "secret "+ref.String()+" belongs to another zone than this store")
	}
	return nil
}

// Format prints the store as its file and zone, never its key.
func (s *Store) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprintf(state, "local.Store(%s %s)", s.path, s.zone) // the state reports no error worth acting on
}

// load reads and opens the store file. A missing file is an empty store; a
// file that others than its owner can read or write is refused.
func (s *Store) load() (document, error) {
	sealed, err := readOwnerOnly(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return document{}, nil
	}
	if err != nil {
		return document{}, err
	}
	aead, err := s.aead()
	if err != nil {
		return document{}, err
	}
	rest, ok := bytes.CutPrefix(sealed, fileMagic)
	if !ok || len(rest) < aead.NonceSize()+aead.Overhead() {
		return document{}, tampered(s.path)
	}
	nonce, ciphertext := rest[:aead.NonceSize()], rest[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, fileMagic)
	if err != nil {
		return document{}, tampered(s.path)
	}
	defer clear(plain)
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return document{}, tampered(s.path)
	}
	return doc, nil
}

// save seals the document and writes it in place of the store file.
func (s *Store) save(doc document) error {
	slices.SortFunc(doc.Entries, func(a, b entry) int { return strings.Compare(a.Ref.Name, b.Ref.Name) })
	plain, err := json.Marshal(doc)
	if err != nil {
		return core.NewError(core.CodeInternal, "the secret store cannot be encoded").WithCause(err)
	}
	defer clear(plain)
	aead, err := s.aead()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(s.random, nonce); err != nil {
		return unavailable(err, "no random bytes are available to seal the secret store")
	}
	sealed := append(slices.Clone(fileMagic), nonce...)
	sealed = aead.Seal(sealed, nonce, plain, fileMagic)
	return writeOwnerOnly(s.path, sealed)
}

// aead builds the AES-GCM cipher from the key.
func (s *Store) aead() (cipher.AEAD, error) {
	key := s.key.Reveal()
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "the secret store key is unusable").WithCause(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "the secret store cipher is unusable").WithCause(err)
	}
	return aead, nil
}

func invalid(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}

func unavailable(cause error, message string) *core.Error {
	return core.NewError(core.CodeUnavailable, message).WithCause(cause)
}

// tampered is the one error for a store that does not open: changed bytes
// and the wrong key look the same, and neither reveals anything.
func tampered(path string) *core.Error {
	return core.NewError(core.CodeTampered, "the secret store "+path+" cannot be authenticated: it was changed or the key is wrong")
}
