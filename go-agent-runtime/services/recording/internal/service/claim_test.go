package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func claimCapture(t *testing.T, destination string) recording.DestinationClaim {
	t.Helper()
	claim, err := New(clock.Real{}).Claim(recording.ClaimOptions{Destination: destination, Kind: recording.ClaimKindCapture})
	if err != nil {
		t.Fatalf("Claim = %v", err)
	}
	return claim
}

func writeFlush(content string) func(string) error {
	return func(path string) error { return os.WriteFile(path, []byte(content), 0o600) }
}

func assertNoTemporaries(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary publish file %q left behind", entry.Name())
		}
	}
}

func TestCaptureClaimPublishesAtomicallyAndNeverReplaces(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "capture.json")
	claim := claimCapture(t, destination)
	if claim.Destination() != destination {
		t.Fatalf("Destination = %q", claim.Destination())
	}
	// A concurrent invocation cannot claim the same destination.
	_, err := New(clock.Real{}).Claim(recording.ClaimOptions{Destination: destination, Kind: recording.ClaimKindCapture})
	var claimErr *recording.ClaimError
	if !errors.Is(err, recording.ErrLiveEvidenceClaimed) || !errors.As(err, &claimErr) || claimErr.Holder == nil || claimErr.Holder.PID != os.Getpid() {
		t.Fatalf("second Claim = %v, want claimed with holder", err)
	}
	if err := claim.Publish(writeFlush("first")); err != nil {
		t.Fatalf("Publish = %v", err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "first" {
		t.Fatalf("published = %q, %v", data, err)
	}
	if err := claim.Publish(writeFlush("second")); !errors.Is(err, recording.ErrLiveEvidenceClaimed) {
		t.Fatalf("republish = %v, want refusal to replace", err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "first" {
		t.Fatalf("destination replaced with %q (%v)", data, err)
	}
	assertNoTemporaries(t, directory)
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	if err := claim.Release(); err != nil {
		t.Fatalf("second Release = %v", err)
	}
	if _, err := os.Stat(destination + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock remains after Release: %v", err)
	}
	if err := claim.Publish(writeFlush("late")); !errors.Is(err, recording.ErrClaimLost) {
		t.Fatalf("Publish after Release = %v", err)
	}
}

func TestCaptureClaimFlushFailureLeavesNoArtifact(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "capture.json")
	claim := claimCapture(t, destination)
	defer requireReleased(t, claim)
	flushErr := errors.New("flush failed")
	if err := claim.Publish(func(string) error { return flushErr }); !errors.Is(err, flushErr) {
		t.Fatalf("Publish = %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed publish created the destination")
	}
	assertNoTemporaries(t, directory)
}

func TestCaptureClaimStolenDuringFlushIsNotPublished(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "capture.json")
	claim := claimCapture(t, destination)
	err := claim.Publish(func(path string) error {
		if err := os.Remove(destination + ".lock"); err != nil {
			return err
		}
		if err := os.WriteFile(destination+".lock", []byte("{}"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("x"), 0o600)
	})
	if !errors.Is(err, recording.ErrClaimLost) {
		t.Fatalf("Publish = %v, want claim lost", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("publish after losing the claim created the destination")
	}
	assertNoTemporaries(t, directory)
	if err := claim.Release(); !errors.Is(err, recording.ErrClaimLost) {
		t.Fatalf("Release of a stolen claim = %v", err)
	}
	// The new owner's lock must survive the old owner's release.
	if _, err := os.Stat(destination + ".lock"); err != nil {
		t.Fatalf("stolen lock removed: %v", err)
	}
}

func TestCaptureClaimLostBeforePublishSkipsFlush(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "capture.json")
	claim := claimCapture(t, destination)
	if err := os.Remove(destination + ".lock"); err != nil {
		t.Fatal(err)
	}
	called := false
	err := claim.Publish(func(string) error { called = true; return nil })
	if !errors.Is(err, recording.ErrClaimLost) || called {
		t.Fatalf("Publish = %v (flush called %v)", err, called)
	}
	if err := claim.Release(); !errors.Is(err, recording.ErrClaimLost) {
		t.Fatalf("Release of a lost claim = %v", err)
	}
}

func TestCaptureClaimRejectsNilFlush(t *testing.T) {
	claim := claimCapture(t, filepath.Join(t.TempDir(), "capture.json"))
	defer requireReleased(t, claim)
	if err := claim.Publish(nil); !errors.Is(err, recording.ErrClaimLost) {
		t.Fatalf("Publish(nil) = %v", err)
	}
}

func TestDirectoryClaimPublishesInPlace(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "bundle")
	claim, err := New(clock.Real{}).Claim(recording.ClaimOptions{Destination: destination, Kind: recording.ClaimKindDirectory})
	if err != nil {
		t.Fatal(err)
	}
	defer requireReleased(t, claim)
	var flushed string
	if err := claim.Publish(func(path string) error { flushed = path; return nil }); err != nil {
		t.Fatal(err)
	}
	if flushed != destination {
		t.Fatalf("flushed %q, want the directory itself", flushed)
	}
}

func TestClaimRejectsInvalidDestinations(t *testing.T) {
	directory := t.TempDir()
	cases := []recording.ClaimOptions{
		{Destination: filepath.Join(directory, "x"), Kind: "other"},
		{Destination: "", Kind: recording.ClaimKindCapture},
		{Destination: directory, Kind: recording.ClaimKindCapture},
	}
	for _, options := range cases {
		claim, err := New(clock.Real{}).Claim(options)
		var claimErr *recording.ClaimError
		if claim != nil || !errors.As(err, &claimErr) {
			t.Errorf("Claim(%+v) = %v, %v; want ClaimError", options, claim, err)
		}
	}
}

func requireReleased(t *testing.T, claim recording.DestinationClaim) {
	t.Helper()
	if err := claim.Release(); err != nil {
		t.Errorf("Release = %v", err)
	}
}
