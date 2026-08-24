package main

import (
	"fmt"
	"os"
)

func main() {
	selector := os.Getenv("FUNCTIONAL_FIXTURE_SELECTOR")
	fmt.Printf("fixture-ran selector=%s\n", selector)
	if os.Getenv("FUNCTIONAL_FIXTURE_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "quarantine-sentinel-executed")
		os.Exit(42)
	}
}
