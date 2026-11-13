package strictjson

import (
	"strings"
	"testing"
)

type sample struct {
	Name  string   `json:"name"`
	Tags  []string `json:"tags"`
	Inner struct {
		Count int `json:"count"`
	} `json:"inner"`
}

// Decode accepts a document spelled as the type is and refuses what two
// readers could read differently or what the type does not name.
func TestDecodeRefusesWhatAReaderCouldGuessAt(t *testing.T) {
	t.Parallel()
	var ok sample
	if err := Decode([]byte(`{"name":"a","tags":["x"],"inner":{"count":2}}`), "the doc", &ok); err != nil || ok.Inner.Count != 2 {
		t.Fatalf("a well-formed document: %v, %+v", err, ok)
	}
	for name, c := range map[string]struct{ doc, says string }{
		"duplicate key":          {`{"name":"a","name":"b"}`, "more than once"},
		"case-variant duplicate": {`{"name":"a","NAME":"b"}`, "more than once"},
		"case-variant only":      {`{"Name":"a"}`, "not a field"},
		"nested case variant":    {`{"inner":{"Count":1}}`, "not a field"},
		"unknown field":          {`{"note":"x"}`, "not a field"},
		"trailing data":          {`{"name":"a"} {}`, "after the JSON document"},
		"truncated":              {`{"name":`, "not valid JSON"},
		"too deep":               {strings.Repeat("[", MaxDepth+2) + strings.Repeat("]", MaxDepth+2), "nests too deeply"},
	} {
		var out sample
		err := Decode([]byte(c.doc), "the doc", &out)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want an error saying %q", name, err, c.says)
		}
	}
}
