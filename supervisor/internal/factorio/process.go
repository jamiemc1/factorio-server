package factorio

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Process wraps the Factorio server child process (the base image's
// /docker-entrypoint.sh, which ultimately execs the Factorio binary). It
// captures stdout/stderr line-by-line into a LogBus while still echoing every
// line to the supervisor's own stdout so `fly logs` keeps working.
type Process struct {
	bus     *LogBus
	cmd     *exec.Cmd
	path    string
	args    []string
	logf    func(string, ...any)
	mu      sync.Mutex
	started bool
	waitErr error
	done    chan struct{}
}

// NewProcess prepares (but does not start) the Factorio process.
func NewProcess(bus *LogBus, path string, args []string, logf func(string, ...any)) *Process {
	return &Process{bus: bus, path: path, args: args, logf: logf, done: make(chan struct{})}
}

// Start launches the child process and begins pumping its output into the bus.
func (p *Process) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return fmt.Errorf("process already started")
	}

	cmd := exec.Command(p.path, p.args...)
	cmd.Env = os.Environ()
	// Put the child in its own process group so we can signal it cleanly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", p.path, err)
	}
	p.cmd = cmd
	p.started = true

	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() { defer pumps.Done(); p.pump(stdout, StreamStdout, os.Stdout) }()
	go func() { defer pumps.Done(); p.pump(stderr, StreamStderr, os.Stderr) }()

	go func() {
		pumps.Wait()
		werr := cmd.Wait()
		p.mu.Lock()
		p.waitErr = werr
		p.mu.Unlock()
		close(p.done)
	}()

	return nil
}

// pump reads r line-by-line, publishing each line to the bus and mirroring it
// to echo (the container's real stdout/stderr).
func (p *Process) pump(r io.Reader, stream Stream, echo io.Writer) {
	scanner := bufio.NewScanner(r)
	// Factorio can emit long lines (e.g. stack traces); allow up to 1 MiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(echo, line)
		p.bus.Publish(stream, line)
	}
	if err := scanner.Err(); err != nil {
		p.bus.Publish(stream, fmt.Sprintf("[supervisor] error reading %s: %v", stream, err))
	}
}

// Wait blocks until the process exits and returns its exit error (nil on clean
// exit).
func (p *Process) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

// Done returns a channel closed when the process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Pid returns the child pid, or 0 if not started.
func (p *Process) Pid() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Signal sends sig to the child process.
func (p *Process) Signal(sig os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return fmt.Errorf("process not running")
	}
	return p.cmd.Process.Signal(sig)
}

// Stop asks the child to terminate (SIGTERM, which triggers a Factorio
// autosave) and waits up to grace for it to exit, escalating to SIGKILL.
// It returns how long the child took to exit.
func (p *Process) Stop(grace time.Duration) time.Duration {
	start := time.Now()
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return time.Since(start)
	}
	select {
	case <-p.done:
		return time.Since(start)
	case <-time.After(grace):
		p.logf("Factorio did not exit after %s, sending SIGKILL", grace)
		_ = p.Signal(syscall.SIGKILL)
		<-p.done
		return time.Since(start)
	}
}
