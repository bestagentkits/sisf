package appbundle

import (
	"errors"
	"fmt"
	"io"
)

// maxTrailerBytes bounds the zero padding read after a tar's end marker
// (tar tools pad to a 10 KiB record; this allows far more).
const maxTrailerBytes = 1 << 20

// endMarkerSize is the two zero blocks that end a tar.
const endMarkerSize = 2 * 512

// countingReader counts the bytes read and keeps the last endMarkerSize.
type countingReader struct {
	r    io.Reader
	n    int64
	tail []byte
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	c.tail = append(c.tail, p[:n]...)
	if len(c.tail) > endMarkerSize {
		c.tail = append(c.tail[:0], c.tail[len(c.tail)-endMarkerSize:]...)
	}
	return n, err
}

func (c *countingReader) endsWithMarker() bool {
	if len(c.tail) < endMarkerSize {
		return false
	}
	for _, b := range c.tail {
		if b != 0 {
			return false
		}
	}
	return true
}

// checkTrailer reads what follows the end marker: only zero bytes, bounded.
func checkTrailer(r io.Reader) error {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := r.Read(buf)
		for _, c := range buf[:n] {
			if c != 0 {
				return errors.New("bundle has data after its end marker")
			}
		}
		if total += int64(n); total > maxTrailerBytes {
			return fmt.Errorf("%w: bundle padding is larger than %d bytes", errLimit, maxTrailerBytes)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read bundle: %w", err)
		}
	}
}

// streamErr reports a stream cut by the overall bound as a limit breach.
func streamErr(err error, c *countingReader, maxStream int64) error {
	if c.n > maxStream {
		return fmt.Errorf("%w: bundle stream is larger than %d bytes", errLimit, maxStream)
	}
	if err == nil {
		return io.ErrUnexpectedEOF
	}
	return err
}
