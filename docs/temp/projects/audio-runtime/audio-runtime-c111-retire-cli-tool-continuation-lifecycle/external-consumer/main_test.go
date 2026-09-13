package main

import "testing"

func TestPublicContractFromSeparateModule(t *testing.T) {
	report, err := assertContract()
	if err != nil {
		t.Fatal(err)
	}
	if report["status"] != "accepted" {
		t.Fatalf("status = %v, want accepted", report["status"])
	}
}
