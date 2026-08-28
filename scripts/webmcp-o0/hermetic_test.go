package main

import "testing"

func TestHermeticStateExpressionsContainIndependentOperations(t *testing.T) {
	stateExpression := hermeticStateExpression()
	transitionExpression := hermeticTransitionExpression()

	if stateExpression == transitionExpression {
		t.Fatal("state and transition expressions unexpectedly share the same operation")
	}
	for _, expression := range []string{stateExpression, transitionExpression} {
		if expression == "" {
			t.Fatal("hermetic expression is empty")
		}
	}
}

func TestHermeticExpectedTransition(t *testing.T) {
	if hermeticInitialValue == hermeticFinalValue {
		t.Fatalf("initial and final fixture values must differ: %q", hermeticInitialValue)
	}
	if hermeticInitialValue != "initial" || hermeticFinalValue != "transitioned" {
		t.Fatalf("fixture values = %q -> %q, want initial -> transitioned", hermeticInitialValue, hermeticFinalValue)
	}
}
