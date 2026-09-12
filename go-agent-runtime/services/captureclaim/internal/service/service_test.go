package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
)

func TestAcquirePublishesDurablyAndReleasesIdempotently(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "capture.json")
	service := New(captureclaim.Dependencies{
		Clock:   fixedClock{now: time.Date(2026, 9, 12, 1, 2, 3, 4, time.UTC)},
		Host:    fixedHost{name: "test-host"},
		Process: fixedProcess{pid: 4242},
	})
	claim, err := service.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claim.Path(), filepath.Clean(path); got != want {
		t.Fatalf("claim path = %q, want %q", got, want)
	}
	lockPath := filepath.Clean(path) + captureclaim.Suffix
	lockInfo, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lockInfo.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("claim mode = %o, want %o", got, want)
	}
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	var holder captureclaim.ClaimHolder
	if err := json.Unmarshal(data, &holder); err != nil {
		t.Fatal(err)
	}
	if holder.RequestedPath != filepath.Clean(path) || holder.PID != 4242 || holder.Host != "test-host" || holder.StartedAtUTC != "2026-09-12T01:02:03.000000004Z" {
		t.Fatalf("holder = %+v", holder)
	}
	if err := claim.Publish(func(tempPath string) error {
		return os.WriteFile(tempPath, []byte("durable bytes"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, []byte("durable bytes")) {
		t.Fatalf("published bytes = %q, err=%v", got, err)
	}
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim sidecar after release = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	assertNoTempFiles(t, path)
}

func TestAcquireCompetingProcessesReportOnlyRedactedHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	deps := captureclaim.Dependencies{HolderObservationAttempts: 4}
	first := New(deps)
	second := New(deps)
	claim, err := first.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseClaimForTest(t, claim)
	secondClaim, err := second.Acquire(path)
	if secondClaim != nil {
		releaseClaimForTest(t, secondClaim)
		t.Fatal("competing process acquired an existing claim")
	}
	var claimErr *captureclaim.ClaimError
	if !errors.As(err, &claimErr) || !errors.Is(err, captureclaim.ErrDestinationClaimed) {
		t.Fatalf("competing error = %T %v", err, err)
	}
	if claimErr.Holder == nil || claimErr.Holder.PID <= 0 || claimErr.Holder.RequestedPath != filepath.Clean(path) {
		t.Fatalf("competing holder = %+v", claimErr.Holder)
	}
	message := err.Error()
	for _, secret := range []string{"prompt", "credential", "arguments", "secret-canary"} {
		if strings.Contains(strings.ToLower(message), secret) {
			t.Fatalf("holder error leaked %q: %s", secret, message)
		}
	}
}

func TestConcurrentAcquireHasOneOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	start := make(chan struct{})
	results := make(chan struct {
		claim captureclaim.Claim
		err   error
	}, 2)
	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			<-start
			claim, err := New(captureclaim.Dependencies{}).Acquire(path)
			results <- struct {
				claim captureclaim.Claim
				err   error
			}{claim: claim, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	owners := 0
	for result := range results {
		if result.claim != nil {
			owners++
			if err := result.claim.Release(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if !errors.Is(result.err, captureclaim.ErrDestinationClaimed) {
			t.Fatalf("concurrent loser error = %v, want claimed", result.err)
		}
	}
	if owners != 1 {
		t.Fatalf("concurrent owners = %d, want one", owners)
	}
}

func TestAcquireRejectsExistingDestinationAndPreservesBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	want := []byte("existing artifact")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	claim, err := New(captureclaim.Dependencies{}).Acquire(path)
	if claim != nil || !errors.Is(err, captureclaim.ErrDestinationOccupied) {
		t.Fatalf("existing destination claim=%v err=%v", claim, err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("existing bytes = %q, err=%v", got, readErr)
	}
}

func TestPublishRejectsDestinationThatAppearsAfterAcquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	claim, err := New(captureclaim.Dependencies{}).Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseClaimForTest(t, claim)
	want := []byte("competitor")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	err = claim.Publish(func(tempPath string) error { return os.WriteFile(tempPath, []byte("claimant"), 0o600) })
	if !errors.Is(err, captureclaim.ErrDestinationOccupied) {
		t.Fatalf("publish error = %v, want occupied", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("competitor bytes = %q, err=%v", got, readErr)
	}
	assertNoTempFiles(t, path)
}

// This fake models an overwrite-prone Link mutation. The clean service must
// reject the destination before Link is reached, so the oracle is behavioral
// rather than a source-text assertion.
func TestPublishNoOverwriteMutationOracle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	fileSystem := &overwriteOnLinkFileSystem{}
	claim, err := New(captureclaim.Dependencies{FileSystem: fileSystem}).Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseClaimForTest(t, claim)
	want := []byte("competitor")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claim.Publish(func(tempPath string) error {
		return os.WriteFile(tempPath, []byte("mutant"), 0o600)
	}); !errors.Is(err, captureclaim.ErrDestinationOccupied) {
		t.Fatalf("mutation oracle error = %v, want occupied", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("mutation oracle destination = %q, err=%v", got, err)
	}
}

func TestPublishDetectsReplacedClaimAndReleasePreservesReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "capture.json")
	claim, err := New(captureclaim.Dependencies{}).Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := path + captureclaim.Suffix
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement")
	if err := os.WriteFile(lockPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	err = claim.Publish(func(tempPath string) error { return os.WriteFile(tempPath, []byte("stale"), 0o600) })
	if !errors.Is(err, captureclaim.ErrClaimLost) {
		t.Fatalf("stale publish error = %v, want claim lost", err)
	}
	if err := claim.Release(); !errors.Is(err, captureclaim.ErrClaimLost) {
		t.Fatalf("stale release error = %v, want claim lost", err)
	}
	got, readErr := os.ReadFile(lockPath)
	if readErr != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("replacement sidecar = %q, err=%v", got, readErr)
	}
	assertNoTempFiles(t, path)
}

func TestPublishDetectsDeletedClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	claim, err := New(captureclaim.Dependencies{}).Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + captureclaim.Suffix); err != nil {
		t.Fatal(err)
	}
	if err := claim.Publish(func(tempPath string) error {
		return os.WriteFile(tempPath, []byte("stale"), 0o600)
	}); !errors.Is(err, captureclaim.ErrClaimLost) {
		t.Fatalf("deleted claim publish error = %v, want claim lost", err)
	}
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	assertNoTempFiles(t, path)
}

// A stale-owner removal mutation must not delete a replacement sidecar.
func TestReleaseReplacementMutationOracle(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "capture.json")
	fileSystem := &faultFileSystem{}
	claim, err := New(captureclaim.Dependencies{FileSystem: fileSystem}).Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := path + captureclaim.Suffix
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	want := []byte("replacement")
	if err := os.WriteFile(lockPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claim.Release(); !errors.Is(err, captureclaim.ErrClaimLost) {
		t.Fatalf("replacement release error = %v, want claim lost", err)
	}
	got, err := os.ReadFile(lockPath)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("replacement after release = %q, err=%v", got, err)
	}
}

func TestObserveHolderIsBoundedForMalformedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json"+captureclaim.Suffix)
	if err := os.WriteFile(path, []byte(`{"pid":42`), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := &countingClock{}
	service := New(captureclaim.Dependencies{
		Clock:                     clock,
		HolderObservationAttempts: 3,
		HolderObservationInterval: time.Nanosecond,
	})
	if holder := service.ObserveHolder(path); holder != nil {
		t.Fatalf("malformed holder = %+v", holder)
	}
	if got, want := clock.sleeps, 2; got != want {
		t.Fatalf("observation sleeps = %d, want %d", got, want)
	}
}

func TestAcquireFailureMatrixPreservesCausesAndCleansClaims(t *testing.T) {
	tests := []struct {
		name  string
		cause error
		setup func(*faultFileSystem, error)
		run   func(*Service, string) error
	}{
		{
			name:  "metadata write",
			cause: errors.New("metadata write cause"),
			setup: func(fs *faultFileSystem, cause error) { fs.failWrite = cause },
			run: func(service *Service, path string) error {
				_, err := service.Acquire(path)
				return err
			},
		},
		{
			name:  "metadata sync",
			cause: errors.New("metadata sync cause"),
			setup: func(fs *faultFileSystem, cause error) { fs.failSyncAt = 1; fs.syncErr = cause },
			run: func(service *Service, path string) error {
				_, err := service.Acquire(path)
				return err
			},
		},
		{
			name:  "metadata close",
			cause: errors.New("metadata close cause"),
			setup: func(fs *faultFileSystem, cause error) { fs.failCloseAt = 1; fs.closeErr = cause },
			run: func(service *Service, path string) error {
				_, err := service.Acquire(path)
				return err
			},
		},
		{
			name:  "retain open",
			cause: errors.New("retain open cause"),
			setup: func(fs *faultFileSystem, cause error) { fs.failReadOnlyAt = 1; fs.openErr = cause },
			run: func(service *Service, path string) error {
				_, err := service.Acquire(path)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capture.json")
			fileSystem := &faultFileSystem{}
			tt.setup(fileSystem, tt.cause)
			err := tt.run(New(captureclaim.Dependencies{FileSystem: fileSystem}), path)
			if err == nil || !errors.Is(err, tt.cause) {
				t.Fatalf("error = %v, want cause %v", err, tt.cause)
			}
			if _, statErr := os.Stat(path + captureclaim.Suffix); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("claim survivor = %v", statErr)
			}
		})
	}
}

func TestPublishFailureMatrixPreservesCausesAndCleansTemporaryFiles(t *testing.T) {
	tests := []struct {
		name  string
		cause error
		setup func(*faultFileSystem, error)
		run   func(captureclaim.Claim, error) error
	}{
		{
			name:  "temporary create",
			cause: errors.New("temporary create cause"),
			setup: func(fs *faultFileSystem, cause error) { fs.createTempErr = cause },
			run: func(claim captureclaim.Claim, _ error) error {
				return claim.Publish(func(string) error { return nil })
			},
		},
		{
			name:  "flush",
			cause: errors.New("flush cause"),
			setup: func(_ *faultFileSystem, _ error) {},
			run: func(claim captureclaim.Claim, cause error) error {
				return claim.Publish(func(string) error { return cause })
			},
		},
		{
			name:  "durable open",
			cause: errors.New("durable open cause"),
			setup: func(fs *faultFileSystem, cause error) { fs.failReadOnlyAt = 2; fs.openErr = cause },
			run: func(claim captureclaim.Claim, _ error) error {
				return claim.Publish(func(path string) error { return os.WriteFile(path, []byte("capture"), 0o600) })
			},
		},
		{
			name:  "durable sync",
			cause: errors.New("durable sync cause"),
			setup: func(fs *faultFileSystem, cause error) { fs.failSyncAt = 2; fs.syncErr = cause },
			run: func(claim captureclaim.Claim, _ error) error {
				return claim.Publish(func(path string) error { return os.WriteFile(path, []byte("capture"), 0o600) })
			},
		},
		{
			name:  "durable close",
			cause: errors.New("durable close cause"),
			setup: func(fs *faultFileSystem, cause error) { fs.failCloseAt = 3; fs.closeErr = cause },
			run: func(claim captureclaim.Claim, _ error) error {
				return claim.Publish(func(path string) error { return os.WriteFile(path, []byte("capture"), 0o600) })
			},
		},
		{
			name:  "link",
			cause: errors.New("link cause"),
			setup: func(fs *faultFileSystem, cause error) { fs.linkErr = cause },
			run: func(claim captureclaim.Claim, _ error) error {
				return claim.Publish(func(path string) error { return os.WriteFile(path, []byte("capture"), 0o600) })
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capture.json")
			fileSystem := &faultFileSystem{}
			tt.setup(fileSystem, tt.cause)
			service := New(captureclaim.Dependencies{FileSystem: fileSystem})
			claim, err := service.Acquire(path)
			if err != nil {
				t.Fatal(err)
			}
			err = tt.run(claim, tt.cause)
			releaseClaimForTest(t, claim)
			if err == nil || !errors.Is(err, tt.cause) {
				t.Fatalf("error = %v, want cause %v", err, tt.cause)
			}
			assertNoTempFiles(t, path)
		})
	}
}

func TestPublishSyncsBeforeLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	fileSystem := &faultFileSystem{}
	claim, err := New(captureclaim.Dependencies{FileSystem: fileSystem}).Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Publish(func(tempPath string) error {
		return os.WriteFile(tempPath, []byte("capture"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	defer releaseClaimForTest(t, claim)

	fileSystem.mu.Lock()
	events := append([]string(nil), fileSystem.events...)
	fileSystem.mu.Unlock()
	lastSync := -1
	link := -1
	for index, event := range events {
		if strings.HasPrefix(event, "sync:") {
			lastSync = index
		}
		if strings.HasPrefix(event, "link:") {
			link = index
		}
	}
	if lastSync < 0 || link < 0 || lastSync >= link {
		t.Fatalf("publication order = %v, want final sync before link", events)
	}
}

func TestReleaseRetriesAnInjectedRemovalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	fileSystem := &faultFileSystem{removeErr: errors.New("remove cause")}
	claim, err := New(captureclaim.Dependencies{FileSystem: fileSystem}).Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Release(); err == nil || !errors.Is(err, fileSystem.removeErr) {
		t.Fatalf("first release error = %v", err)
	}
	if _, err := os.Stat(path + captureclaim.Suffix); err != nil {
		t.Fatalf("failed release sidecar = %v", err)
	}
	fileSystem.removeErr = nil
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + captureclaim.Suffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar after retry = %v", err)
	}
}

func TestReleaseRetriesTransientOwnershipInspectionFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	fileSystem := &faultFileSystem{}
	claim, err := New(captureclaim.Dependencies{FileSystem: fileSystem}).Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	fileSystem.lstatPath = path + captureclaim.Suffix
	fileSystem.lstatErr = errors.New("inspect cause")
	if err := claim.Release(); err == nil || !errors.Is(err, fileSystem.lstatErr) {
		t.Fatalf("first inspection error = %v", err)
	}
	if _, err := os.Stat(path + captureclaim.Suffix); err != nil {
		t.Fatalf("claim after transient inspection error = %v", err)
	}
	fileSystem.lstatErr = nil
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + captureclaim.Suffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim after inspection retry = %v", err)
	}
}

func TestConcurrentReleaseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	claim, err := New(captureclaim.Dependencies{}).Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	errorsCh := make(chan error, 8)
	var group sync.WaitGroup
	group.Add(cap(errorsCh))
	for range cap(errorsCh) {
		go func() {
			defer group.Done()
			errorsCh <- claim.Release()
		}()
	}
	group.Wait()
	close(errorsCh)
	for releaseErr := range errorsCh {
		if releaseErr != nil {
			t.Errorf("concurrent release error = %v", releaseErr)
		}
	}
	if _, err := os.Stat(path + captureclaim.Suffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar after concurrent release = %v", err)
	}
}

func assertNoTempFiles(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func releaseClaimForTest(t *testing.T, claim captureclaim.Claim) {
	t.Helper()
	if err := claim.Release(); err != nil {
		t.Errorf("release claim: %v", err)
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time    { return c.now }
func (fixedClock) Sleep(time.Duration) {}

type countingClock struct{ sleeps int }

func (c *countingClock) Now() time.Time      { return time.Unix(0, 0) }
func (c *countingClock) Sleep(time.Duration) { c.sleeps++ }

type fixedHost struct{ name string }

func (h fixedHost) Hostname() (string, error) { return h.name, nil }

type fixedProcess struct{ pid int }

func (p fixedProcess) PID() int { return p.pid }
