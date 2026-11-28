package proc

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// envChild marks the test binary run again as the child a test starts,
// and names what the child is to do.
const envChild = "TABC_PROC_TEST_CHILD"

// What a child does.
const (
	// childReport writes childWord to the file it was handed.
	childReport = "report"
	// childWait calls Contain, starts a grandchild that waits, reports
	// the grandchild's pid, and waits to be ended.
	childWait = "wait"
	// childIdle waits to be ended.
	childIdle = "idle"
)

// childWord is what a reporting child writes.
const childWord = "ready"

// endWait is how long an ended process gets to be gone, and endPoll how
// often the test looks.
const (
	endWait = 10 * time.Second
	endPoll = 20 * time.Millisecond
)

// TestMain runs the test binary's other role: started by a test with
// envChild set, the binary is that test's child and runs no tests.
func TestMain(m *testing.M) {
	switch os.Getenv(envChild) {
	case "":
		os.Exit(m.Run())

	case childReport:
		handed := Handed()
		fmt.Fprintln(handed, childWord)
		handed.Close()

	case childWait:
		handed := Handed()
		if err := Contain(); err != nil {
			fmt.Fprintln(handed, "contain:", err)
			os.Exit(1)
		}
		grandchild := exec.Command(os.Args[0])
		grandchild.Env = append(os.Environ(), envChild+"="+childIdle)
		if err := grandchild.Start(); err != nil {
			fmt.Fprintln(handed, "grandchild:", err)
			os.Exit(1)
		}
		fmt.Fprintln(handed, grandchild.Process.Pid)
		handed.Close()
		select {}

	case childIdle:
		select {}
	}
}

// startChild runs the test binary again as a child that leads a group
// of its own and is handed a pipe, and returns the child and the pipe's
// reading end.
func startChild(t *testing.T, role string) (*exec.Cmd, *bufio.Reader) {
	t.Helper()

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { read.Close() })

	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), envChild+"="+role)
	Lead(child, KeepTerminal)
	if err := Hand(child, write); err != nil {
		t.Fatalf("hand: %v", err)
	}
	if err := child.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	write.Close()
	return child, bufio.NewReader(read)
}

// gone waits for a process to stop existing and reports whether it did.
func gone(pid int) bool {
	deadline := time.Now().Add(endWait)
	for time.Now().Before(deadline) {
		if !Alive(pid) {
			return true
		}
		time.Sleep(endPoll)
	}
	return false
}

// TestHand checks that a started process finds the file its starter
// handed over: a child of the test writes a word to the pipe it was
// handed, and the test reads the word from the pipe's other end.
func TestHand(t *testing.T) {
	child, reader := startChild(t, childReport)
	defer child.Wait()

	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading from the child: %v", err)
	}
	if word := strings.TrimSpace(line); word != childWord {
		t.Errorf("the child wrote %q, want %q", word, childWord)
	}
}

// TestAlive checks that a process reads as existing only while it
// runs: this process, a child before and after it is ended, and a pid
// no process has.
func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("the test's own process does not exist")
	}
	if Alive(0) || Alive(-1) {
		t.Error("a pid that is no process's exists")
	}

	child, _ := startChild(t, childIdle)
	pid := child.Process.Pid
	if !Alive(pid) {
		t.Error("a running child does not exist")
	}

	child.Process.Kill()
	child.Wait()
	if !gone(pid) {
		t.Error("a child that ended still exists")
	}
}

// TestKillGroup checks that ending a container's supervisor ends what
// the supervisor started. A child stands in for the supervisor: it
// calls Contain and starts a grandchild, as a supervisor starts a
// browser. The test ends the child with KillGroup and waits for both
// processes to be gone.
func TestKillGroup(t *testing.T) {
	child, reader := startChild(t, childWait)
	defer child.Wait()

	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading from the child: %v", err)
	}
	var grandchild int
	if _, err := fmt.Sscan(line, &grandchild); err != nil {
		t.Fatalf("the child said %q", strings.TrimSpace(line))
	}
	if !Alive(grandchild) {
		t.Fatal("the grandchild does not exist")
	}

	if err := KillGroup(child.Process.Pid); err != nil {
		t.Fatalf("kill group: %v", err)
	}
	child.Wait()
	if !gone(child.Process.Pid) {
		t.Error("the child outlived its group")
	}
	if !gone(grandchild) {
		t.Error("the grandchild outlived its group")
	}
}
