package dockerfilter

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bestagentkits/sisf/internal/strictjson"
)

// scanStrict refuses a create body the daemon's decoder would read
// differently from a careful reader; see strictjson.Scan.
func scanStrict(raw []byte) error { return strictjson.Scan(raw, "the body") }

// objectKeys decodes a JSON object and refuses any key that is not spelled
// exactly as one of allowed. A key in another letter case, which the
// daemon's decoder would accept as the canonical one, is refused too.
func objectKeys(raw json.RawMessage, what string, allowed ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object", what)
	}
	for key := range fields {
		if !slices.Contains(allowed, key) {
			return nil, fmt.Errorf("%s field %q is not allowed", what, key)
		}
	}
	return fields, nil
}
