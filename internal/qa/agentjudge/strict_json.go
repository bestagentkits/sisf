package agentjudge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// jsonFrame is one open object or array while a document is walked.
type jsonFrame struct {
	object    bool
	keys      map[string]bool
	expectKey bool
}

// checkDuplicateKeys walks a JSON document and refuses one whose objects
// repeat a key. encoding/json keeps the last of two equal keys, so a second
// score hidden behind the first would otherwise be read silently. The
// document must be a single object.
func checkDuplicateKeys(doc []byte) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var stack []*jsonFrame
	first := true
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if first {
			if delim, ok := tok.(json.Delim); !ok || delim != '{' {
				return errors.New("the document is not an object")
			}
			first = false
		}
		var top *jsonFrame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.object && top.expectKey {
			if delim, ok := tok.(json.Delim); ok && delim == '}' {
				stack = valueDone(stack[:len(stack)-1])
				continue
			}
			key, _ := tok.(string) // a key is always a string token
			if top.keys[key] {
				return fmt.Errorf("the key %q appears twice", key)
			}
			top.keys[key], top.expectKey = true, false
			continue
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &jsonFrame{object: true, keys: map[string]bool{}, expectKey: true})
		case json.Delim('['):
			stack = append(stack, &jsonFrame{})
		case json.Delim(']'):
			stack = valueDone(stack[:len(stack)-1])
		default:
			stack = valueDone(stack)
		}
	}
}

// valueDone records that the innermost object got the value of its key.
func valueDone(stack []*jsonFrame) []*jsonFrame {
	if len(stack) > 0 && stack[len(stack)-1].object {
		stack[len(stack)-1].expectKey = true
	}
	return stack
}
