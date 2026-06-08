package condition

import (
	"strings"
	"testing"
)

func TestCompile_Valid(t *testing.T) {
	if _, err := Compile(`changeType == "schema"`); err != nil {
		t.Fatalf("valid expression rejected: %v", err)
	}
}

func TestCompile_SyntaxError(t *testing.T) {
	if _, err := Compile(`changeType ===`); err == nil {
		t.Fatal("expected syntax error")
	}
}

func TestCompile_UnknownVariable(t *testing.T) {
	if _, err := Compile(`mystery > 1`); err == nil {
		t.Fatal("expected error for unknown variable")
	}
}

func TestCompile_NonBoolResult(t *testing.T) {
	// A condition must yield bool; a bare string must be rejected at compile time.
	if _, err := Compile(`criticality`); err == nil {
		t.Fatal("expected error: condition must evaluate to bool")
	}
}

func TestEval_True(t *testing.T) {
	got, err := Eval(`criticality == "high" && blastRadius > 5`, Input{
		Criticality: "high",
		BlastRadius: 8,
	})
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if !got {
		t.Fatal("expected true")
	}
}

func TestEval_False(t *testing.T) {
	got, err := Eval(`environment == "prod" && changeType == "schema"`, Input{
		Environment: "staging",
		ChangeType:  "schema",
	})
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got {
		t.Fatal("expected false")
	}
}

func TestEval_AllVariables(t *testing.T) {
	// Every declared variable must be referenceable without error.
	_, err := Eval(
		`criticality == "low" || environment == "dev" || changeType == "config" || blastRadius == 0 || strategy == "rolling" || score < 0.5`,
		Input{Criticality: "low", Environment: "dev", ChangeType: "config", BlastRadius: 0, Strategy: "rolling", Score: 0.1},
	)
	if err != nil {
		t.Fatalf("Eval over all vars: %v", err)
	}
}

func TestEval_ReusesCompiledProgram(t *testing.T) {
	p, err := Compile(`score >= threshold`)
	if err == nil {
		t.Fatal("threshold is not a declared variable; expected compile error")
	}
	_ = p

	p2, err := Compile(`score >= 0.7`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got, err := p2.EvalBool(Input{Score: 0.9})
	if err != nil {
		t.Fatalf("EvalBool: %v", err)
	}
	if !got {
		t.Fatal("expected 0.9 >= 0.7 true")
	}
}

func TestCompile_ErrorMentionsExpression(t *testing.T) {
	_, err := Compile(`bad ===`)
	if err == nil || !strings.Contains(err.Error(), "condition") {
		t.Fatalf("error should be namespaced to condition, got: %v", err)
	}
}
