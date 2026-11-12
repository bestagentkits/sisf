package dockerfilter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
)

// maxJSONDepth bounds how deeply a create body may nest.
const maxJSONDepth = 16

// scanStrict walks a JSON document and refuses what the daemon's decoder
// would read differently from a careful reader: a key that appears twice in
// one object, also when the two spellings differ only in letter case (the
// daemon's decoder matches field names without regard to case, and the last
// one wins), nesting deeper than maxJSONDepth, and anything after the
// document.
func scanStrict(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("the body has data after the JSON document")
	}
	return nil
}

func walkValue(dec *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errors.New("the body nests too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		return errors.New("the body is not valid JSON")
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]string{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return errors.New("the body is not valid JSON")
			}
			key, _ := keyTok.(string) // object keys are always strings
			folded := foldKey(key)
			if first, dup := seen[folded]; dup {
				return fmt.Errorf("key %q appears more than once (also as %q)", key, first)
			}
			seen[folded] = key
			if err := walkValue(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkValue(dec, depth+1); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // the closing delimiter
		return errors.New("the body is not valid JSON")
	}
	return nil
}

// foldKey maps every rune to the smallest rune of its case-folding orbit,
// so two keys fold alike exactly when bytes.EqualFold, which the standard
// decoder uses to match field names, holds for them. That includes the
// Kelvin sign for "k" and the long s for "s".
func foldKey(s string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		return least
	}, s)
}

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
