package appbundle

import (
	"errors"
	"fmt"
)

// Limits bound a bundle, both when Assemble builds it and when Verify reads
// one. Every field must be positive.
type Limits struct {
	// MaxTotalBytes bounds the sum of the bundle's file sizes. It also
	// bounds each downloaded archive.
	MaxTotalBytes int64
	// MaxFiles bounds the number of entries, files and directories alike.
	MaxFiles int
	// MaxFileBytes bounds a single file.
	MaxFileBytes int64
}

func (l Limits) validate() error {
	if l.MaxTotalBytes <= 0 || l.MaxFiles <= 0 || l.MaxFileBytes <= 0 {
		return fmt.Errorf("limits %+v: every limit must be positive", l)
	}
	return nil
}

// errLimit marks a refusal because a limit was reached.
var errLimit = errors.New("bundle limit exceeded")

// budget counts what a bundle has used against its Limits.
type budget struct {
	limits  Limits
	entries int
	bytes   int64
}

// entry counts one more file or directory.
func (b *budget) entry(name string) error {
	b.entries++
	if b.entries > b.limits.MaxFiles {
		return fmt.Errorf("%w: more than %d entries at %q", errLimit, b.limits.MaxFiles, name)
	}
	return nil
}

// nextFileCap is the most bytes the next file may have.
func (b *budget) nextFileCap() int64 {
	return min(b.limits.MaxFileBytes, b.limits.MaxTotalBytes-b.bytes)
}

// fileBytes checks a file of n bytes against the single-file and total
// limits and counts it.
func (b *budget) fileBytes(name string, n int64) error {
	if n > b.limits.MaxFileBytes {
		return fmt.Errorf("%w: %q is larger than %d bytes", errLimit, name, b.limits.MaxFileBytes)
	}
	if b.bytes+n > b.limits.MaxTotalBytes {
		return fmt.Errorf("%w: bundle is larger than %d bytes at %q", errLimit, b.limits.MaxTotalBytes, name)
	}
	b.bytes += n
	return nil
}
