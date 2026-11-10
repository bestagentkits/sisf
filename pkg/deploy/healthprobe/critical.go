package healthprobe

import "regexp"

// criticalLine matches a log line of critical or fatal level, in key=value
// or JSON form.
var criticalLine = regexp.MustCompile(`(?i)(\blevel=(critical|fatal)\b|"level"\s*:\s*"(critical|fatal)")`)

// IsCriticalLine reports whether an application log line is of critical or
// fatal level. A release counts these in the window it measures.
func IsCriticalLine(line string) bool { return criticalLine.MatchString(line) }
