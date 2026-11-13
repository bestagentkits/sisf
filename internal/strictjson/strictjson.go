// Package strictjson reads JSON the way a careful reader must when two
// readers could disagree on what a document says: a key that appears twice
// in one object (also in another letter case, which the standard decoder
// matches without regard to case), nesting beyond a fixed depth, data after
// the document, and, for Decode, any key that is not a field spelled
// exactly as the target type names it, are refused rather than guessed at.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// MaxDepth bounds how deeply a document may nest.
const MaxDepth = 16

// Scan walks a JSON document and refuses a key that appears more than once
// in one object, also when the spellings differ only in letter case,
// nesting deeper than MaxDepth, and anything after the document. what names
// the document in the error, such as "the body".
func Scan(raw []byte, what string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkValue(dec, 0, what); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s has data after the JSON document", what)
	}
	return nil
}

func walkValue(dec *json.Decoder, depth int, what string) error {
	if depth > MaxDepth {
		return fmt.Errorf("%s nests too deeply", what)
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%s is not valid JSON", what)
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
				return fmt.Errorf("%s is not valid JSON", what)
			}
			key, _ := keyTok.(string) // object keys are always strings
			folded := FoldKey(key)
			if first, dup := seen[folded]; dup {
				return fmt.Errorf("key %q appears more than once (also as %q)", key, first)
			}
			seen[folded] = key
			if err := walkValue(dec, depth+1, what); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkValue(dec, depth+1, what); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // the closing delimiter
		return fmt.Errorf("%s is not valid JSON", what)
	}
	return nil
}

// FoldKey maps every rune to the smallest rune of its case-folding orbit,
// so two keys fold alike exactly when bytes.EqualFold, which the standard
// decoder uses to match field names, holds for them. That includes the
// Kelvin sign for "k" and the long s for "s".
func FoldKey(s string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		return least
	}, s)
}

// Decode scans raw with Scan and decodes it into out, refusing a key that
// is not a field of out. A key the standard decoder would match to a field
// only by ignoring letter case is refused too: out is encoded again and
// every key of raw must appear, spelled the same, at the same place in it.
// A field marked omitempty that raw sets to its zero value is therefore
// refused as well, which no encoder of out produces.
func Decode(raw []byte, what string, out any) error {
	if err := Scan(raw, what); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		var field string
		if _, err := fmt.Sscanf(strings.TrimPrefix(err.Error(), "json: unknown field "), "%q", &field); err == nil {
			return fmt.Errorf("%s: %q is not a field", what, field)
		}
		return fmt.Errorf("%s does not have the expected shape: %v", what, err)
	}
	again, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("%s cannot be encoded again: %v", what, err)
	}
	var have, want any
	if json.Unmarshal(raw, &have) != nil || json.Unmarshal(again, &want) != nil {
		return fmt.Errorf("%s is not valid JSON", what)
	}
	if path, key, ok := firstStrayKey(have, want, ""); ok {
		return fmt.Errorf("%s: %q at %s is not a field spelled as such", what, key, path)
	}
	return nil
}

// firstStrayKey finds a key of have that want does not hold at the same
// place, spelled the same.
func firstStrayKey(have, want any, path string) (string, string, bool) {
	switch h := have.(type) {
	case map[string]any:
		w, _ := want.(map[string]any)
		for key, value := range h {
			next, ok := w[key]
			if !ok {
				return orRoot(path), key, true
			}
			if p, k, stray := firstStrayKey(value, next, path+"."+key); stray {
				return p, k, true
			}
		}
	case []any:
		w, _ := want.([]any)
		for i, value := range h {
			var next any
			if i < len(w) {
				next = w[i]
			}
			if p, k, stray := firstStrayKey(value, next, fmt.Sprintf("%s[%d]", path, i)); stray {
				return p, k, true
			}
		}
	}
	return "", "", false
}

func orRoot(path string) string {
	if path == "" {
		return "the top level"
	}
	return path
}
