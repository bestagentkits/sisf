package install

import (
	"encoding/json"
	"strings"

	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/pkg/core"
)

// writeStandaloneTrust places the trust files of a Standalone install on
// its trust volume dir. A release the supervisor promoted stays the last
// known good release of its unit when the supervisor's program store on
// that volume still holds its program, unchanged: the factory then comes
// back serving what it promoted, not the application the install builds.
//
// An entry whose program is gone or changed is removed, as every promoted
// entry was before, and the operator is told that the application starts
// from the installed release again: the supervisor would not run that
// program, and without the entry it has a release it can run. The
// simulation, whose promotions never ran a program of their own, is not
// told.
func (in *Installer) writeStandaloneTrust(dir string, files map[string][]byte, sim bool) error {
	store, err := programstore.Open(dir)
	if err != nil {
		return err
	}
	return writeTrustFiles(dir, files, func(name string, entry []byte) bool {
		unit := strings.TrimSuffix(strings.TrimPrefix(name, promotedLKGPrefix), ".json")
		var promoted struct {
			Unit   string      `json:"unit"`
			Digest core.Digest `json:"digest"`
		}
		reason := "its record cannot be read"
		if json.Unmarshal(entry, &promoted) == nil && promoted.Unit == unit && promoted.Digest.Validate() == nil {
			_, err := store.Path(promoted.Digest)
			if err == nil {
				return true
			}
			reason = "the supervisor's program store does not hold its program unchanged (" + string(core.CodeOf(err)) + ")"
		}
		if !sim {
			in.say("the promoted release %s of %s is dropped: %s; the application starts from the installed release again\n",
				promoted.Digest, unit, reason)
		}
		return false
	})
}
