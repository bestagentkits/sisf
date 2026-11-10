package web

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Table is a list of records shown as columns and rows. Actions whose answer
// has no fixed shape in this package, such as the product catalog, are shown
// this way, so a page lists what the action returned and adds nothing.
type Table struct {
	Cols []string
	Rows [][]string
}

// maxTableRows bounds a rendered table.
const maxTableRows = 500

// tableFrom finds the list in an action output, an array or the first array
// of objects held by an object, and shows each element as a row. A scalar
// field is a cell; a nested value is shown as compact JSON.
func tableFrom(raw json.RawMessage) Table {
	var records []map[string]any
	var top any
	if len(raw) == 0 || json.Unmarshal(raw, &top) != nil {
		return Table{}
	}
	records = recordsIn(top)
	cols := columnsOf(records)
	t := Table{Cols: cols}
	for i, rec := range records {
		if i == maxTableRows {
			break
		}
		row := make([]string, len(cols))
		for j, c := range cols {
			row[j] = cell(rec[c])
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func recordsIn(v any) []map[string]any {
	switch x := v.(type) {
	case []any:
		return recordsOf(x)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if list, ok := x[k].([]any); ok {
				return recordsOf(list)
			}
		}
	}
	return nil
}

func recordsOf(list []any) []map[string]any {
	var out []map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// columnsOf is the union of the keys of the records, sorted.
func columnsOf(records []map[string]any) []string {
	seen := map[string]bool{}
	var cols []string
	for _, rec := range records {
		for k := range rec {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	sort.Strings(cols)
	return cols
}

func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
