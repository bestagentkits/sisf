package cliagent

import "time"

// SetDrainGrace changes the silence after a run's exit that closes its
// output on a straggler, for runs b starts from now on.
func SetDrainGrace(b *Backend, grace time.Duration) { b.drainGrace = grace }
