package core

import (
	"errors"
	"slices"
	"testing"
)

// passingReceipt records every required hard gate as passed and a score of 90
// that its five rubric categories add up to.
func passingReceipt() QAReceipt {
	receipt := validReceipt()
	receipt.Checks = nil
	for _, gate := range RequiredHardGates() {
		receipt.Checks = append(receipt.Checks, QACheck{Name: gate, Kind: CheckHardGate, Passed: true})
	}
	receipt.Checks = append(receipt.Checks,
		QACheck{Name: "correctness_regression", Kind: CheckRubric, Passed: true, Points: 28, MaxPoints: 30},
		QACheck{Name: "security_auth", Kind: CheckRubric, Passed: true, Points: 23, MaxPoints: 25},
		QACheck{Name: "contract_data_safety", Kind: CheckRubric, Passed: true, Points: 18, MaxPoints: 20},
		QACheck{Name: "ux_performance_output", Kind: CheckRubric, Passed: true, Points: 12, MaxPoints: 15},
		QACheck{Name: "observability_rollback", Kind: CheckRubric, Passed: true, Points: 9, MaxPoints: 10},
	)
	receipt.Score = 90
	return receipt
}

// gateIndex finds the hard-gate result of the named gate.
func gateIndex(r QAReceipt, gate string) int {
	return slices.IndexFunc(r.Checks, func(c QACheck) bool { return c.Kind == CheckHardGate && c.Name == gate })
}

// errorDetails returns the details of a typed error, or nil.
func errorDetails(err error) map[string]string {
	var typed *Error
	if !errors.As(err, &typed) {
		return nil
	}
	return typed.Details
}

func TestTheRequiredHardGatesAreAClosedSetOfNine(t *testing.T) {
	t.Parallel()
	want := []string{"tests_contract", "permissions", "migrations", "secrets", "observed_build_confidence",
		"independent_evaluator", "resource_budget_policy", "rollback_readiness", "supply_chain"}
	if got := RequiredHardGates(); !slices.Equal(got, want) {
		t.Fatalf("RequiredHardGates() = %v, want %v", got, want)
	}
	RequiredHardGates()[0] = "none"
	if RequiredHardGates()[0] != "tests_contract" {
		t.Fatal("a caller removed a required hard gate for everyone")
	}
}

func TestAReceiptPassesWithEveryRequiredGateAndAScoreAtThePassScore(t *testing.T) {
	t.Parallel()
	if err := passingReceipt().CheckPass(85); err != nil {
		t.Fatalf("a receipt with every gate passed and 90 points was refused at 85: %v", err)
	}
	if err := passingReceipt().CheckPass(90); err != nil {
		t.Fatalf("a score exactly at the pass score was refused: %v", err)
	}
	if got := passingReceipt().RubricPoints(); got != 90 {
		t.Fatalf("RubricPoints() = %d, want the 90 points of the five categories", got)
	}
}

func TestAReceiptDoesNotPassWithoutEveryRequiredHardGate(t *testing.T) {
	t.Parallel()
	for _, gate := range RequiredHardGates() {
		missing := passingReceipt()
		missing.Checks = slices.Delete(missing.Checks, gateIndex(missing, gate), gateIndex(missing, gate)+1)
		err := missing.CheckPass(85)
		if !HasCode(err, CodeGuardFailed) {
			t.Errorf("gate %s was never run: want %s, got %v", gate, CodeGuardFailed, err)
			continue
		}
		if named := errorDetails(err)["gate"]; named != gate {
			t.Errorf("gate %s was never run: the refusal names gate %q", gate, named)
		}

		failed := passingReceipt()
		failed.Checks[gateIndex(failed, gate)].Passed = false
		if err := failed.CheckPass(1); !HasCode(err, CodeGuardFailed) {
			t.Errorf("gate %s failed: want %s even at the lowest pass score, got %v", gate, CodeGuardFailed, err)
		}

		relabelled := passingReceipt()
		relabelled.Checks[gateIndex(relabelled, gate)].Kind = CheckRubric
		if err := relabelled.CheckPass(85); !HasCode(err, CodeGuardFailed) {
			t.Errorf("gate %s recorded only as a scored category: want %s, got %v", gate, CodeGuardFailed, err)
		}
	}
}

func TestNoHardGateMayFailWhateverTheScore(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*QAReceipt){
		"a gate outside the required set failed": func(r *QAReceipt) {
			r.Checks = append(r.Checks, QACheck{Name: "license_scan", Kind: CheckHardGate, Passed: false})
		},
		"a required gate ran twice and failed once": func(r *QAReceipt) {
			r.Checks = append(r.Checks, QACheck{Name: "secrets", Kind: CheckHardGate, Passed: false})
		},
		"a perfect score with a failed gate": func(r *QAReceipt) {
			r.Checks[gateIndex(*r, "rollback_readiness")].Passed = false
			for i := range r.Checks {
				if r.Checks[i].Kind == CheckRubric {
					r.Checks[i].Points = r.Checks[i].MaxPoints
				}
			}
			r.Score = 100
		},
		"no hard gate was recorded at all": func(r *QAReceipt) {
			r.Checks = slices.DeleteFunc(r.Checks, func(c QACheck) bool { return c.Kind == CheckHardGate })
		},
		"no check was recorded at all": func(r *QAReceipt) { r.Checks, r.Score = nil, 0 },
	}
	for name, mutate := range cases {
		receipt := passingReceipt()
		mutate(&receipt)
		if err := receipt.CheckPass(1); !HasCode(err, CodeGuardFailed) {
			t.Errorf("%s: want %s, got %v", name, CodeGuardFailed, err)
		}
	}
}

func TestAnExtraGateOfTheCriteriaMustPassToo(t *testing.T) {
	t.Parallel()
	if err := passingReceipt().CheckPass(85, "license_scan"); !HasCode(err, CodeGuardFailed) {
		t.Fatalf("an extra gate that never ran: want %s, got %v", CodeGuardFailed, err)
	}
	if err := passingReceipt().CheckPass(85, ""); !HasCode(err, CodeGuardFailed) {
		t.Fatalf("an unnamed extra gate: want %s, got %v", CodeGuardFailed, err)
	}
	unnamed := passingReceipt()
	unnamed.Checks = append(unnamed.Checks, QACheck{Kind: CheckHardGate, Passed: true})
	if err := unnamed.CheckPass(85, ""); !HasCode(err, CodeGuardFailed) {
		t.Fatalf("an unnamed gate result answering an unnamed extra gate: want %s, got %v", CodeGuardFailed, err)
	}
	with := passingReceipt()
	with.Checks = append(with.Checks, QACheck{Name: "license_scan", Kind: CheckHardGate, Passed: true})
	if err := with.CheckPass(85, "license_scan"); err != nil {
		t.Fatalf("an extra gate that passed was refused: %v", err)
	}
	// Extra gates add to the required set and never replace it.
	only := passingReceipt()
	only.Checks = slices.Delete(only.Checks, gateIndex(only, "secrets"), gateIndex(only, "secrets")+1)
	if err := only.CheckPass(85, "tests_contract"); !HasCode(err, CodeGuardFailed) {
		t.Fatalf("naming one gate dropped the others: want %s, got %v", CodeGuardFailed, err)
	}
}

func TestTheScoreMustEqualTheSumOfTheRubricPoints(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*QAReceipt){
		"score claims more than the points earned": func(r *QAReceipt) { r.Score = 95 },
		"score claims one point more":              func(r *QAReceipt) { r.Score = 91 },
		"score claims less than the points earned": func(r *QAReceipt) { r.Score = 89 },
		"score without any rubric points": func(r *QAReceipt) {
			r.Checks = slices.DeleteFunc(r.Checks, func(c QACheck) bool { return c.Kind == CheckRubric })
		},
		"points recorded on a hard gate do not count": func(r *QAReceipt) {
			r.Checks[gateIndex(*r, "secrets")].Points = 5
			r.Score = 95
		},
	}
	for name, mutate := range cases {
		receipt := passingReceipt()
		mutate(&receipt)
		if err := receipt.CheckPass(85); !HasCode(err, CodeGuardFailed) {
			t.Errorf("%s: want %s, got %v", name, CodeGuardFailed, err)
		}
	}
}

func TestAScoreBelowThePassScoreDoesNotPass(t *testing.T) {
	t.Parallel()
	if err := passingReceipt().CheckPass(91); !HasCode(err, CodeGuardFailed) {
		t.Fatalf("90 points against a pass score of 91: want %s, got %v", CodeGuardFailed, err)
	}
	for _, passScore := range []int{0, -5, 101} {
		if err := passingReceipt().CheckPass(passScore); !HasCode(err, CodeGuardFailed) {
			t.Errorf("pass score %d is outside 1-100: want %s, got %v", passScore, CodeGuardFailed, err)
		}
	}
}

func TestAZeroReceiptDoesNotPass(t *testing.T) {
	t.Parallel()
	if err := (QAReceipt{}).CheckPass(1); !HasCode(err, CodeGuardFailed) {
		t.Fatalf("an empty receipt: want %s, got %v", CodeGuardFailed, err)
	}
}
