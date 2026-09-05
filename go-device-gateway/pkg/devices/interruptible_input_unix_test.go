//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package devices

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestOpenInterruptibleInputDuplicatesPipeAndHonorsDeadline(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()

	dup, err := OpenInterruptibleInput(read)
	if err != nil {
		t.Fatalf("OpenInterruptibleInput() = %v", err)
	}
	defer dup.Close()
	if err := dup.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline() = %v", err)
	}
	var one [1]byte
	if _, err := dup.Read(one[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read() error = %v, want deadline exceeded", err)
	}
	if _, err := write.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	if err := dup.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear read deadline = %v", err)
	}
	if count, err := dup.Read(one[:]); err != nil || count != 1 || one[0] != 'x' {
		t.Fatalf("Read() after write = (%d, %v), byte %q; want one x", count, err, one[0])
	}
}

// This test exercises the descriptor shape produced by a real child process.
// Darwin can leave a direct os.Stdin pipe in a syscall.Read after its owner
// closes the file; the duplicated nonblocking descriptor must instead wake on
// its deadline and let the session cancellation path make progress.
func TestOpenInterruptibleInputInheritedPipeSubprocess(t *testing.T) {
	if os.Getenv("GO_AGENT_INTERRUPTIBLE_INPUT_CHILD") == "1" {
		inherited := os.NewFile(uintptr(3), "inherited-pipe")
		if inherited == nil {
			os.Exit(2)
		}
		defer inherited.Close()
		dup, err := OpenInterruptibleInput(inherited)
		if err != nil {
			os.Exit(3)
		}
		defer dup.Close()
		if err := dup.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			os.Exit(4)
		}
		var one [1]byte
		if _, err := dup.Read(one[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
			os.Exit(5)
		}
		return
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestOpenInterruptibleInputInheritedPipeSubprocess$")
	cmd.Env = append(os.Environ(), "GO_AGENT_INTERRUPTIBLE_INPUT_CHILD=1")
	cmd.ExtraFiles = []*os.File{read}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inherited-pipe child = %v, output %s", err, output)
	}
}
