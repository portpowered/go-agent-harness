package parity

import "testing"

func TestFormatReportZeroFindings(t *testing.T) {
	want := "Parity comparison: 0 differences (projections agree)\n"
	if got := FormatReport(nil); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
}

func TestFormatReportMultiDifferenceGolden(t *testing.T) {
	differences := []Difference{
		{Path: "audio.frames[0].payload", Expected: `"YQ=="`, Actual: `"Yg=="`},
		{Path: "terminal.reason", Expected: `"provider_close"`, Actual: `"client_close"`},
		{Path: "transcripts[0].tick", Expected: "12", Actual: "99"},
	}
	want := "Parity comparison: 3 differences\n" +
		"  1. audio.frames[0].payload: expected \"YQ==\"; actual \"Yg==\"\n" +
		"  2. terminal.reason: expected \"provider_close\"; actual \"client_close\"\n" +
		"  3. transcripts[0].tick: expected 12; actual 99\n"

	got := FormatReport(differences)
	if got != want {
		t.Fatalf("report mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}

	differences[0].Path = "changed.after.format"
	if got != want {
		t.Fatal("report retained a mutable caller alias")
	}
}
