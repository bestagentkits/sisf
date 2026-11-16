package install

import (
	"io"
	"os"
	"strings"
)

// Bounds of the log end a refused unit is reported with.
const (
	logTailBytes = 4096
	logTailLines = 3
)

// unitLogTail returns the last lines of a unit's log, or "" when it cannot
// be read. A unit's log holds what it chose to print, which is redacted
// before it is written, so it may be shown on the output of `sisf up`.
func (in *Installer) unitLogTail(name string) string {
	f, err := os.Open(in.Layout.Logs() + "/" + name + ".log")
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }() // read only
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := max(info.Size()-logTailBytes, 0)
	raw, err := io.ReadAll(io.NewSectionReader(f, start, info.Size()-start))
	if err != nil {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, "  "+line)
		}
	}
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line may be cut
	}
	return strings.Join(lines[max(len(lines)-logTailLines, 0):], "\n")
}
