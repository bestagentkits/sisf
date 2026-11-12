package local

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/pkg/core"
)

// NewKeyFile writes a fresh random key to path for its owner only. It
// refuses to replace a key that exists: the store it opens would be lost.
func NewKeyFile(path string, random io.Reader) error {
	if random == nil {
		random = rand.Reader
	}
	key := make([]byte, keySize)
	defer clear(key)
	if _, err := io.ReadFull(random, key); err != nil {
		return unavailable(err, "no random bytes are available to make a key")
	}
	if _, err := os.Lstat(path); err == nil {
		return core.NewError(core.CodeAlreadyExists, "the key file "+path+" already exists")
	}
	encoded := []byte(base64.RawURLEncoding.EncodeToString(key))
	defer clear(encoded)
	return writeOwnerOnly(path, encoded)
}

// readKey reads and checks the key file.
func readKey(path string) (core.SecretValue, error) {
	text, err := readOwnerOnly(path)
	if errors.Is(err, fs.ErrNotExist) {
		return core.SecretValue{}, core.NewError(core.CodeNotFound, "the key file "+path+" does not exist")
	}
	if err != nil {
		return core.SecretValue{}, err
	}
	defer clear(text)
	key, err := base64.RawURLEncoding.DecodeString(string(bytes.TrimSpace(text)))
	if err != nil || len(key) != keySize {
		clear(key)
		return core.SecretValue{}, invalid("the key file %s does not hold a %d-byte key in URL-safe base64", path, keySize)
	}
	defer clear(key)
	return core.NewSecretValue(key), nil
}

// maxFileBytes bounds what is read from a key or store file.
const maxFileBytes = 16 << 20

// readOwnerOnly reads a regular file that only its owner can read or write.
// The mode is taken from the opened file, so it is the file that is read. A
// missing file returns an error matching fs.ErrNotExist.
func readOwnerOnly(path string) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return nil, unavailable(err, "cannot open "+path)
	}
	defer func() { _ = file.Close() }() // read-only: a failed close loses nothing
	info, err := file.Stat()
	if err != nil {
		return nil, unavailable(err, "cannot inspect "+path)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, core.NewError(core.CodePermissionDenied,
			fmt.Sprintf("%s must be a regular file only its owner can access (mode %o, want %o)", path, info.Mode().Perm(), fileMode))
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, unavailable(err, "cannot read "+path)
	}
	if len(data) > maxFileBytes {
		clear(data)
		return nil, invalid("%s is larger than %d bytes", path, maxFileBytes)
	}
	return data, nil
}

// writeOwnerOnly writes data to path for its owner only. The file is written
// beside its target with its mode set first and renamed into place, so a
// reader never sees a partial file.
func writeOwnerOnly(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return unavailable(err, "cannot create "+dir)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return unavailable(err, "cannot create a file in "+dir)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone after a successful rename; cleanup after a failure
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return unavailable(err, "cannot restrict "+path)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return unavailable(err, "cannot write "+path)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return unavailable(err, "cannot flush "+path)
	}
	if err := tmp.Close(); err != nil {
		return unavailable(err, "cannot write "+path)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return unavailable(err, "cannot place "+path)
	}
	return nil
}
