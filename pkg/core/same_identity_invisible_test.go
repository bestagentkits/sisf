package core

import "testing"

func TestAnIdentityCannotBeRespelledWithInvisibleCharacters(t *testing.T) {
	t.Parallel()
	coder := "agent:coder-1"
	for name, evaluator := range map[string]string{
		"a zero-width space inside":      "agent:coder\u200b-1",
		"a zero-width joiner inside":     "agent:\u200dcoder-1",
		"a zero-width non-joiner inside": "agent:coder-\u200c1",
		"a word joiner at the end":       "agent:coder-1\u2060",
		"a byte order mark in front":     "\ufeffagent:coder-1",
		"a soft hyphen inside":           "agent:co\u00adder-1",
		"a right-to-left mark inside":    "agent:\u200fcoder-1",
		"a no-break space at the end":    "agent:coder-1\u00a0",
		"a control character inside":     "agent:coder\x00-1",
		"several of them":                "\u200b Agent:\u200dCODER-1\u2060 ",
	} {
		if !SameIdentity(evaluator, coder) || !SameIdentity(coder, evaluator) {
			t.Errorf("%s: %q and %q name the same party", name, evaluator, coder)
		}
		r := validReceipt()
		r.CodingAgentIdentity, r.QAAgentIdentity = coder, evaluator
		if CodeOf(r.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: evaluator %q scored coder %q: %v", name, evaluator, coder, r.Validate())
		}
	}
	for _, other := range []string{"agent:coder-2", "agent:coder-10", "agent:coder", "agent:qa-1"} {
		if SameIdentity(other, coder) {
			t.Errorf("%q was taken for %q", other, coder)
		}
	}
}
