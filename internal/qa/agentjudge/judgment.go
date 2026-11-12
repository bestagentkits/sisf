package agentjudge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// The structured channel: the QA agent's final message must hold exactly one
// judgment between these markers, and nothing else is ever read as one.
const (
	OpenMarker  = "<sisf_judgment>"
	CloseMarker = "</sisf_judgment>"
	// JudgmentVersion is the only wire format version accepted.
	JudgmentVersion = 1
)

// Bounds of a judgment. The whole block stays below what the agent adapters
// keep of one message (8 KiB), so a judgment is never read cut short.
const (
	MaxJudgmentBytes = 7000
	MaxEvidenceBytes = 500
	MaxNoteBytes     = 200
	MaxSummaryBytes  = 500
)

// ErrInvalidJudgment is the cause of every refused judgment, so a caller can
// tell a judgment that broke the contract from an agent that never answered.
var ErrInvalidJudgment = errors.New("the QA agent's judgment breaks the judgment contract")

// CriterionScore is the agent's score of one rubric criterion.
type CriterionScore struct {
	Points   int    `json:"points"`
	Evidence string `json:"evidence"`
}

// GateObservation is what the agent noticed about one hard gate. It is
// advisory: the deterministic gates decide; the evaluator records them.
type GateObservation struct {
	Gate    qa.GateName `json:"gate"`
	Concern bool        `json:"concern"`
	Note    string      `json:"note,omitempty"`
}

// Judgment is a validated judgment: one score per rubric criterion, each
// within the criterion's weight and backed by an evidence note. It holds no
// total and no verdict; the evaluator applies weights, the pass score and
// the hard gates.
type Judgment struct {
	Scores       map[qa.Category]CriterionScore `json:"scores"`
	Observations []GateObservation              `json:"hard_gate_observations,omitempty"`
	Summary      string                         `json:"summary,omitempty"`
}

// Scorecard turns the judgment into the scorecard the evaluator reads.
func (j Judgment) Scorecard(evidenceRef string) qa.Scorecard {
	card := qa.Scorecard{Points: map[qa.Category]int{}, Details: map[qa.Category]string{}, EvidenceRef: evidenceRef}
	for category, score := range j.Scores {
		card.Points[category] = score.Points
		card.Details[category] = score.Evidence
	}
	return card
}

// wireJudgment is the JSON inside the markers. Every field is decoded
// strictly: unknown fields, duplicate keys and trailing data are refused.
type wireJudgment struct {
	Version      *int                 `json:"version"`
	Scores       map[string]wireScore `json:"scores"`
	Observations []wireObservation    `json:"hard_gate_observations"`
	Summary      string               `json:"summary"`
}

type wireScore struct {
	Points   *int    `json:"points"`
	Evidence *string `json:"evidence"`
}

type wireObservation struct {
	Gate    string `json:"gate"`
	Concern *bool  `json:"concern"`
	Note    string `json:"note"`
}

// ParseJudgment reads the judgment from the agent's final message and checks
// it against the rubric. Text outside the markers is ignored; anything else
// that is not exactly one well-formed judgment is refused with
// INVALID_ARGUMENT caused by ErrInvalidJudgment.
func ParseJudgment(message string, rubric qa.Rubric) (Judgment, error) {
	if err := rubric.Validate(); err != nil {
		return Judgment{}, err
	}
	body, err := extractBlock(message)
	if err != nil {
		return Judgment{}, err
	}
	if err := checkDuplicateKeys(body); err != nil {
		return Judgment{}, refuse("the judgment is not a JSON object with unique keys: %v", err)
	}
	var wire wireJudgment
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return Judgment{}, refuse("the judgment does not follow the format: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Judgment{}, refuse("the judgment block holds more than one JSON value")
	}
	return wire.validate(rubric)
}

// extractBlock returns the JSON between the only pair of markers.
func extractBlock(message string) ([]byte, error) {
	opens, closes := strings.Count(message, OpenMarker), strings.Count(message, CloseMarker)
	switch {
	case opens == 0 && closes == 0:
		return nil, refuse("the final message holds no judgment")
	case opens != 1 || closes != 1:
		return nil, refuse("the final message must hold exactly one judgment, it holds %d opening and %d closing markers", opens, closes)
	}
	start := strings.Index(message, OpenMarker) + len(OpenMarker)
	end := strings.Index(message, CloseMarker)
	if end < start {
		return nil, refuse("the judgment's closing marker comes before its opening one")
	}
	body := strings.TrimSpace(message[start:end])
	body = stripCodeFence(body)
	switch {
	case len(body) > MaxJudgmentBytes:
		return nil, refuse("the judgment is larger than %d bytes", MaxJudgmentBytes)
	case !utf8.ValidString(body):
		return nil, refuse("the judgment is not valid UTF-8")
	}
	return []byte(body), nil
}

// stripCodeFence removes one Markdown code fence around the JSON, which
// models add out of habit; the JSON inside is still read strictly.
func stripCodeFence(body string) string {
	if !strings.HasPrefix(body, "```") || !strings.HasSuffix(body, "```") || len(body) < 6 {
		return body
	}
	inner := strings.TrimSuffix(body[3:], "```")
	if tag, rest, ok := strings.Cut(inner, "\n"); ok && (tag == "" || tag == "json") {
		return strings.TrimSpace(rest)
	}
	return body
}

// validate checks the decoded judgment against the rubric.
func (w wireJudgment) validate(rubric qa.Rubric) (Judgment, error) {
	if w.Version == nil || *w.Version != JudgmentVersion {
		return Judgment{}, refuse("the judgment must state version %d", JudgmentVersion)
	}
	if len(w.Scores) != len(rubric.Weights) {
		return Judgment{}, refuse("the judgment must score exactly the %d rubric criteria", len(rubric.Weights))
	}
	j := Judgment{Scores: map[qa.Category]CriterionScore{}}
	for _, category := range qa.Categories() {
		weight := rubric.Weights[category]
		score, ok := w.Scores[string(category)]
		switch {
		case !ok:
			return Judgment{}, refuse("the judgment does not score %s", category)
		case score.Points == nil:
			return Judgment{}, refuse("the judgment gives %s no points", category)
		case *score.Points < 0 || *score.Points > weight:
			return Judgment{}, refuse("the judgment scores %s outside 0-%d", category, weight)
		case score.Evidence == nil || strings.TrimSpace(*score.Evidence) == "":
			return Judgment{}, refuse("the judgment gives no evidence for %s", category)
		case len(*score.Evidence) > MaxEvidenceBytes:
			return Judgment{}, refuse("the evidence for %s is longer than %d bytes", category, MaxEvidenceBytes)
		}
		j.Scores[category] = CriterionScore{Points: *score.Points, Evidence: strings.TrimSpace(*score.Evidence)}
	}
	if len(w.Summary) > MaxSummaryBytes {
		return Judgment{}, refuse("the summary is longer than %d bytes", MaxSummaryBytes)
	}
	j.Summary = strings.TrimSpace(w.Summary)
	gates := qa.Gates()
	for _, o := range w.Observations {
		gate := qa.GateName(o.Gate)
		switch {
		case !slices.Contains(gates, gate):
			return Judgment{}, refuse("%q is not a hard gate", o.Gate)
		case slices.ContainsFunc(j.Observations, func(seen GateObservation) bool { return seen.Gate == gate }):
			return Judgment{}, refuse("the judgment observes the %s gate more than once", gate)
		case o.Concern == nil:
			return Judgment{}, refuse("the observation of the %s gate does not say whether it is a concern", gate)
		case len(o.Note) > MaxNoteBytes:
			return Judgment{}, refuse("the note on the %s gate is longer than %d bytes", gate, MaxNoteBytes)
		}
		j.Observations = append(j.Observations, GateObservation{Gate: gate, Concern: *o.Concern, Note: strings.TrimSpace(o.Note)})
	}
	return j, nil
}

// refuse builds the error of a judgment that breaks the contract.
func refuse(format string, args ...any) error {
	return core.NewError(core.CodeInvalidArgument, "the QA agent's judgment was refused: "+fmt.Sprintf(format, args...)).
		WithCause(ErrInvalidJudgment)
}
