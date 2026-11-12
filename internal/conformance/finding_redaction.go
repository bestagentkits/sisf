package conformance

import (
	"reflect"
	"strings"
)

// withheldValue stands in a finding for a value that has the shape of a
// secret.
const withheldValue = "(a value withheld: it has the shape of a secret)"

// withheldArg returns a value to be formatted into a finding, with any text
// that has the shape of a secret withheld: text, a type whose underlying
// type is text, such as a unit name or a digest, and a list of either.
func withheldArg(arg any) any {
	value := reflect.ValueOf(arg)
	switch {
	case !value.IsValid():
		return arg
	case value.Kind() == reflect.String:
		if secretShape(value.String()) != "" {
			return withheldValue
		}
	case value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.String:
		texts := make([]string, value.Len())
		changed := false
		for i := range texts {
			texts[i] = value.Index(i).String()
			if secretShape(texts[i]) != "" {
				texts[i], changed = withheldValue, true
			}
		}
		if changed {
			return texts
		}
	}
	return arg
}

// withheldText returns text, such as a finding's place or fix, with every
// word that has the shape of a secret withheld. When the shape spans words,
// as an Authorization header does, the whole text is withheld.
func withheldText(text string) string {
	if secretShape(text) == "" {
		return text
	}
	words := strings.Fields(text)
	for i, word := range words {
		if secretShape(strings.Trim(word, `"'`)) != "" || secretShape(word) != "" {
			words[i] = withheldValue
		}
	}
	if joined := strings.Join(words, " "); secretShape(joined) == "" {
		return joined
	}
	return withheldValue
}
