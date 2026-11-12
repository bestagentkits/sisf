//go:build !linux

package processstart

// systemStart returns when pid started, as ps prints it.
func systemStart(pid int) (string, error) { return psStart(pid) }

// procStart answers for a record whose start time was read from /proc,
// which only Linux has: no process here started at such a time, so the
// record names no live process.
func procStart(int) (string, error) { return "", nil }
