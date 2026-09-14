package main

import "testing"

func TestExternalConsumerContract(t *testing.T) {
	report, err := assertContract()
	if err != nil {
		t.Fatal(err)
	}
	if report["status"] != "accepted" || report["wire"] != true {
		t.Fatalf("consumer report = %#v", report)
	}
}
