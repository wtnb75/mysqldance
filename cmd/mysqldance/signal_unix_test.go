//go:build unix

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestSecondSignalHelper is the child process of TestSecondSignalKills. It is
// a no-op unless started by that test.
func TestSecondSignalHelper(t *testing.T) {
	if os.Getenv("MYSQLDANCE_SIGNAL_HELPER") != "1" {
		t.Skip("helper process")
	}
	ctx, stop := notifyContext(context.Background())
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
	// Give notifyContext time to give the signals back.
	time.Sleep(200 * time.Millisecond)
	fmt.Println("canceled")
	time.Sleep(30 * time.Second)
}

// After the first signal canceled the run, a second one must terminate the
// process, so a hung cleanup cannot swallow docker stop or kill.
func TestSecondSignalKills(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecondSignalHelper$")
	cmd.Env = append(os.Environ(), "MYSQLDANCE_SIGNAL_HELPER=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	sc := bufio.NewScanner(out)
	wait := func(want string) {
		t.Helper()
		for sc.Scan() {
			if sc.Text() == want {
				return
			}
		}
		t.Fatalf("child never printed %q", want)
	}
	wait("ready")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	wait("canceled")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("child exited with %v, want death by signal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second SIGTERM did not terminate the process")
	}
}

// SIGTERM (sent by docker stop and kill) must cancel the run the same way as
// Ctrl-C, so the dump takes its cleanup path.
func TestNotifyContextCancelsOnSignals(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, stop := notifyContext(context.Background())
			defer stop()
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatalf("context not canceled by %v", sig)
			}
		})
	}
}
