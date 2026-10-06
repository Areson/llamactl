package instance

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// process manages the OS process lifecycle for a local instance.
// process owns its complete lifecycle including auto-restart logic.
type process struct {
	instance *Instance // Back-reference for SetStatus, GetOptions

	mu            sync.RWMutex
	cmd           *exec.Cmd
	ctx           context.Context
	cancel        context.CancelFunc
	stdin         io.Closer
	stdout        io.ReadCloser
	stderr        io.ReadCloser
	childLog      *os.File    // Windows: child stdout/stderr, survives parent death
	job           *processJob // Windows: Job Object for kill-tree; nil elsewhere
	restarts      int
	restartCancel context.CancelFunc
	monitorDone   chan struct{}
}

// newProcess creates a new process component for the given instance
func newProcess(instance *Instance) *process {
	return &process{
		instance: instance,
	}
}

// start starts the OS process and returns an error if it fails.
func (p *process) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.instance.IsRunning() {
		return fmt.Errorf("instance %s is already running", p.instance.Name)
	}

	// Safety check: ensure options are valid
	if p.instance.options == nil {
		return fmt.Errorf("instance %s has no options set", p.instance.Name)
	}

	// Reset restart counter when manually starting (not during auto-restart)
	// We can detect auto-restart by checking if restartCancel is set
	if p.restartCancel == nil {
		p.restarts = 0
	}

	// Initialize last request time to current time when starting
	if p.instance.proxy != nil {
		p.instance.proxy.updateLastRequestTime()
	}

	// Create context before building command (needed for CommandContext)
	p.ctx, p.cancel = context.WithCancel(context.Background())

	// Create log files
	if err := p.instance.logger.create(); err != nil {
		return fmt.Errorf("failed to create log files: %w", err)
	}

	// Build command using backend-specific methods
	cmd, cmdErr := p.buildCommand()
	if cmdErr != nil {
		return fmt.Errorf("failed to build command: %w", cmdErr)
	}
	p.cmd = cmd

	// Double-spawn guard: if our port is already accepting connections, a
	// surviving llama-server from a previous (or crashed) start is still
	// bound to it. Waiting it out avoids double-binding, which would run
	// two copies of the model in VRAM at once.
	host, port := p.instance.options.GetHost(), p.instance.options.GetPort()
	// Dial to loopback regardless of the configured bind host: a server bound
	// to 0.0.0.0/:: is reachable via 127.0.0.1, but net.DialTimeout to
	// 0.0.0.0 (or ::) fails on Windows with an invalid-address error and the
	// guard would silently no-op.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if port > 0 {
		deadline := time.Now().Add(15 * time.Second)
		for portInUse(host, port) {
			if time.Now().After(deadline) {
				p.instance.logger.close()
				if p.cancel != nil {
					p.cancel()
				}
				return fmt.Errorf("port %d is still occupied by a surviving process; refusing to start a second server on it", port)
			}
			log.Printf("Instance %s: port %d still in use, waiting for it to be released...", p.instance.Name, port)
			time.Sleep(500 * time.Millisecond)
		}
	}

	setProcAttrs(p.cmd)

	// Windows: create a Job Object and inherit its handle into the child so
	// stopping/killing tears down the whole tree (Tabby re-exec, uvicorn, etc.)
	// while hot-swap can still leave the tree alive when llamactl A exits.
	// Fresh spawn owns the process; clear any prior adopt flag so stop()
	// uses the Job Object path rather than stopAdopted.
	p.instance.SetAdopted(false)

	if job, jerr := newProcessJob(p.instance.Name); jerr != nil {
		log.Printf("Instance %s: Job Object unavailable (%v); continuing without kill-tree", p.instance.Name, jerr)
	} else if job != nil {
		p.job = job
		p.job.prepareCmd(p.cmd)
	}

	if runtime.GOOS == "windows" {
		// No pipes to the parent: when llamactl is hot-swapped, stdin EOF
		// would stop the model and a broken stdout pipe would lose logs.
		// Redirect to the log file and NUL so the child survives A's exit.
		nul, err := os.OpenFile("NUL", os.O_RDWR, 0)
		if err != nil {
			p.closeJob()
			p.instance.logger.close()
			return fmt.Errorf("failed to open NUL: %w", err)
		}
		p.cmd.Stdin = nul
		logPath := p.instance.logger.path()
		lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			nul.Close()
			p.closeJob()
			p.instance.logger.close()
			return fmt.Errorf("failed to open child log: %w", err)
		}
		p.childLog = lf
		p.cmd.Stdout = lf
		p.cmd.Stderr = lf
	} else {
		var err error
		p.stdin, err = p.cmd.StdinPipe()
		if err != nil {
			p.closeJob()
			p.instance.logger.close()
			return fmt.Errorf("failed to get stdin pipe: %w", err)
		}
		p.stdout, err = p.cmd.StdoutPipe()
		if err != nil {
			p.closeJob()
			p.instance.logger.close()
			return fmt.Errorf("failed to get stdout pipe: %w", err)
		}
		p.stderr, err = p.cmd.StderrPipe()
		if err != nil {
			p.stdout.Close()
			p.closeJob()
			p.instance.logger.close()
			return fmt.Errorf("failed to get stderr pipe: %w", err)
		}
	}

	if err := p.cmd.Start(); err != nil {
		p.closeChildLog()
		p.closeJob()
		return fmt.Errorf("failed to start instance %s: %w", p.instance.Name, err)
	}

	// Assign immediately after Start (best-effort race window; see processJob docs).
	if p.job != nil {
		if aerr := p.job.assign(p.cmd.Process); aerr != nil {
			log.Printf("Instance %s: AssignProcessToJobObject failed (%v); closing job, continuing", p.instance.Name, aerr)
			p.closeJob()
		}
	}

	p.instance.SetStatus(Running)

	// Create channel for monitor completion signaling
	p.monitorDone = make(chan struct{})

	if p.stdout != nil {
		go p.instance.logger.readOutput(p.stdout)
	}
	if p.stderr != nil {
		go p.instance.logger.readOutput(p.stderr)
	}

	go p.monitorProcess()

	p.writeRuntimeState()

	return nil
}

// stop terminates the subprocess without restarting
func (p *process) stop() error {
	p.mu.Lock()

	if !p.instance.IsRunning() {
		// Even if not running, cancel any pending restart
		if p.restartCancel != nil {
			p.restartCancel()
			p.restartCancel = nil
			log.Printf("Cancelled pending restart for instance %s", p.instance.Name)
		}
		p.mu.Unlock()
		return fmt.Errorf("instance %s is not running", p.instance.Name)
	}

	// Cancel any pending restart
	if p.restartCancel != nil {
		p.restartCancel()
		p.restartCancel = nil
	}

	// Set status to ShuttingDown first to reject new requests
	p.instance.SetStatus(ShuttingDown)

	// For adopted instances, the process was started by a prior llamactl
	// generation. We don't have the pipe handles, so we signal the PID
	// directly and wait for the port to release.
	if p.instance.IsAdopted() {
		p.mu.Unlock()
		return p.stopAdopted()
	}

	// Capture this generation's pipes, cmd, job, and monitor channel before
	// releasing the lock. start() may replace p.cmd/p.stdin while the
	// inflight drain below runs; stop() must signal only the process it saw
	// when it locked, never a newer generation.
	cmd := p.cmd
	stdin := p.stdin
	job := p.job
	monitorDone := p.monitorDone
	// Detach job from the process struct so a concurrent restart cannot
	// double-close; this stop owns teardown.
	p.job = nil

	p.mu.Unlock()

	// Wait for inflight requests to complete (max 30 seconds)
	log.Printf("Instance %s shutting down, waiting for inflight requests to complete...", p.instance.Name)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		inflight := p.instance.GetInflightRequests()
		if inflight == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Now set status to stopped to signal intentional stop
	p.instance.SetStatus(Stopped)

	// If no process exists, we can return immediately
	if cmd == nil || monitorDone == nil {
		p.instance.logger.close()
		return nil
	}

	// Clean stop first: close stdin (EOF is a shutdown request where the
	// backend honors it), then the platform interrupt — SIGINT on Unix,
	// Ctrl-C on the child's own console on Windows. Only after the grace
	// period expires is the process (tree) hard-killed. If the request
	// could not be delivered at all, skip straight to the hard kill.
	if stdin != nil {
		if cerr := stdin.Close(); cerr != nil {
			log.Printf("Failed to close stdin for instance %s: %v", p.instance.Name, cerr)
		}
	}
	killGrace := p.gracefulStopTimeout()
	if serr := signalStop(cmd); serr != nil {
		log.Printf("Instance %s: clean stop request failed (%v); hard-killing", p.instance.Name, serr)
		killGrace = 0
	} else {
		log.Printf("Instance %s: clean stop requested; waiting up to %v before hard kill", p.instance.Name, killGrace)
	}

	select {
	case <-monitorDone:
		// Process exited normally
		log.Printf("Instance %s shut down gracefully", p.instance.Name)
	case <-time.After(killGrace):
		// Force kill: prefer Job Object tree kill on Windows so Tabby/uvicorn
		// grandchildren die with the root; fall back to Process.Kill.
		if job != nil {
			if kerr := job.killTree(); kerr != nil {
				log.Printf("Instance %s: TerminateJobObject failed: %v", p.instance.Name, kerr)
			} else {
				log.Printf("Instance %s did not stop in time, job tree terminated", p.instance.Name)
			}
		} else if cmd != nil && cmd.Process != nil {
			killErr := cmd.Process.Kill()
			if killErr != nil {
				log.Printf("Failed to force kill instance %s: %v", p.instance.Name, killErr)
			}
			log.Printf("Instance %s did not stop in time, force killed", p.instance.Name)
		}

		// Wait a bit more for the monitor to finish after force kill
		select {
		case <-monitorDone:
			// Monitor completed after force kill
		case <-time.After(10 * time.Second):
			log.Printf("Warning: Monitor goroutine did not complete after force kill for instance %s", p.instance.Name)
		}
	}

	if job != nil {
		job.close()
	}

	p.instance.logger.close()
	p.closeChildLog()
	p.removeRuntimeState()

	return nil
}

// restart manually restarts the process (resets restart counter)
func (p *process) restart() error {
	// Stop the process first
	if err := p.stop(); err != nil {
		// If it's not running, that's ok - we'll just start it
		if err.Error() != fmt.Sprintf("instance %s is not running", p.instance.Name) {
			return fmt.Errorf("failed to stop instance during restart: %w", err)
		}
	}

	// Reset restart counter for manual restart
	p.mu.Lock()
	p.restarts = 0
	p.mu.Unlock()

	// Start the process
	return p.start()
}

// waitForHealthy waits for the process to become healthy
func (p *process) waitForHealthy(timeout int) error {
	if !p.instance.IsRunning() {
		return fmt.Errorf("instance %s is not running", p.instance.Name)
	}

	if timeout <= 0 {
		timeout = 30 // Default to 30 seconds if no timeout is specified
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Get host/port from instance
	host := p.instance.options.GetHost()
	port := p.instance.options.GetPort()
	healthURL := fmt.Sprintf("http://%s:%d/health", host, port)

	// Create a dedicated HTTP client for health checks
	client := &http.Client{
		Timeout: 5 * time.Second, // 5 second timeout per request
	}

	// Helper function to check health directly
	checkHealth := func() bool {
		req, err := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
		if err != nil {
			return false
		}

		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		return resp.StatusCode == http.StatusOK
	}

	// Try immediate check first
	if checkHealth() {
		return nil // Instance is healthy
	}

	// If immediate check failed, start polling
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for instance %s to become healthy after %d seconds", p.instance.Name, timeout)
		case <-ticker.C:
			if checkHealth() {
				return nil // Instance is healthy
			}
			// Continue polling
		}
	}
}

// monitorProcess monitors the OS process and handles crashes/exits
func (p *process) monitorProcess() {
	// Capture this generation's identity up front so a stale monitor
	// (left over from a superseded start) can never close a newer
	// monitor's completion channel or overwrite live instance state.
	p.mu.Lock()
	myCmd := p.cmd
	myDone := p.monitorDone
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		if myDone != nil {
			// Always close our generation's channel so a stop() waiting on it
			// (even for a superseded process) is released. Clear the field only
			// if this is still the current generation.
			close(myDone)
			if p.monitorDone == myDone {
				p.monitorDone = nil
			}
		}
		p.mu.Unlock()
	}()

	err := myCmd.Wait()

	p.mu.Lock()

	// Stale monitor: a newer start replaced p.cmd while we were waiting.
	// The defer signals our channel; leave the live state untouched.
	if p.cmd != myCmd {
		log.Printf("Instance %s: ignoring exit of superseded process (stale monitor)", p.instance.Name)
		p.mu.Unlock()
		return
	}

	// Check if the instance was intentionally stopped
	if !p.instance.IsRunning() {
		p.mu.Unlock()
		return
	}

	p.instance.SetStatus(Stopped)
	p.instance.logger.close()
	p.closeChildLog()
	p.closeJob()
	p.removeRuntimeState()

	// Cancel any existing restart context since we're handling a new exit
	if p.restartCancel != nil {
		p.restartCancel()
		p.restartCancel = nil
	}

	// Log the exit
	if err != nil {
		log.Printf("Instance %s crashed with error: %v", p.instance.Name, err)
		// Handle auto-restart logic
		p.handleAutoRestart(err)
	} else {
		log.Printf("Instance %s exited cleanly", p.instance.Name)
		p.mu.Unlock()
	}
}

// shouldAutoRestart checks if the process should auto-restart
func (p *process) shouldAutoRestart() bool {
	opts := p.instance.GetOptions()
	if opts == nil {
		log.Printf("Instance %s not restarting: options are nil", p.instance.Name)
		return false
	}

	if opts.AutoRestart == nil || !*opts.AutoRestart {
		log.Printf("Instance %s not restarting: AutoRestart is disabled", p.instance.Name)
		return false
	}

	if opts.MaxRestarts == nil {
		log.Printf("Instance %s not restarting: MaxRestarts is nil", p.instance.Name)
		return false
	}

	maxRestarts := *opts.MaxRestarts
	if p.restarts >= maxRestarts {
		log.Printf("Instance %s exceeded max restart attempts (%d)", p.instance.Name, maxRestarts)
		return false
	}

	return true
}

// handleAutoRestart manages the auto-restart process
func (p *process) handleAutoRestart(err error) {
	// Check if should restart
	if !p.shouldAutoRestart() {
		p.instance.SetStatus(Failed)
		p.mu.Unlock()
		return
	}

	// Get restart parameters
	opts := p.instance.GetOptions()
	if opts.RestartDelay == nil {
		log.Printf("Instance %s not restarting: RestartDelay is nil", p.instance.Name)
		p.instance.SetStatus(Failed)
		p.mu.Unlock()
		return
	}

	restartDelay := *opts.RestartDelay
	maxRestarts := *opts.MaxRestarts

	p.restarts++

	// Set status to Restarting instead of leaving as Stopped
	p.instance.SetStatus(Restarting)

	log.Printf("Auto-restarting instance %s (attempt %d/%d) in %v",
		p.instance.Name, p.restarts, maxRestarts, time.Duration(restartDelay)*time.Second)

	// Create a cancellable context for the restart delay
	restartCtx, cancel := context.WithCancel(context.Background())
	p.restartCancel = cancel

	// Release the lock before sleeping
	p.mu.Unlock()

	// Use context-aware sleep so it can be cancelled
	select {
	case <-time.After(time.Duration(restartDelay) * time.Second):
		// Sleep completed normally, continue with restart
	case <-restartCtx.Done():
		// Restart was cancelled
		log.Printf("Restart cancelled for instance %s", p.instance.Name)
		return
	}

	// Restart the instance
	if err := p.start(); err != nil {
		log.Printf("Failed to restart instance %s: %v", p.instance.Name, err)
	} else {
		log.Printf("Successfully restarted instance %s", p.instance.Name)
		// Clear the cancel function
		p.mu.Lock()
		p.restartCancel = nil
		p.mu.Unlock()
	}
}

// buildCommand builds the command to execute using backend-specific logic
func (p *process) buildCommand() (*exec.Cmd, error) {

	// Build the environment variables
	env := p.instance.buildEnvironment()

	// Get the command to execute
	command := p.instance.getCommand()

	// Build command arguments
	args := p.instance.buildCommandArgs()

	// Create the exec.Cmd
	cmd := exec.CommandContext(p.ctx, command, args...)

	// TabbyAPI (and any backend that returns a working dir) needs cwd at the
	// install root so relative assets like sampler_overrides/ resolve correctly.
	if dir := p.instance.workingDir(args); dir != "" {
		cmd.Dir = dir
	}

	// Start with host environment variables
	cmd.Env = os.Environ()

	// Add/override with backend-specific environment variables
	for k, v := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	return cmd, nil
}

// portInUse reports whether something is accepting TCP connections on host:port.
func portInUse(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// stopAdopted stops an instance that was adopted from a prior llamactl
// generation. The process was started by a different llamactl, so we don't
// have the pipe handles. Instead we:
//  1. Read the PID from runtime.json
//  2. Request a clean stop (console Ctrl-C on Windows, SIGINT on Unix) and
//     wait up to the graceful stop timeout for the PID to exit.
//  3. If it is still running: on Windows prefer TerminateJobObject via the
//     named Job Object (kill-tree); fall back to assigning descendants into a
//     new job or TerminateProcess on each descendant. On Unix: SIGTERM the
//     root PID.
//  4. Wait for the port to release (up to 30s)
//  5. Clear the adopted flag so a later start/stop uses the owned-process path.
func (p *process) stopAdopted() error {
	host, port := p.instance.options.GetHost(), p.instance.options.GetPort()
	if host == "" {
		host = "127.0.0.1"
	}

	// Find the runtime state to get the PID.
	instanceDir := filepath.Join(p.instance.globalInstanceSettings.InstancesDir, p.instance.Name)
	state, err := ReadRuntimeState(instanceDir)
	if err != nil {
		p.instance.SetStatus(Failed)
		return fmt.Errorf("failed to read runtime state for adopted instance %s: %w", p.instance.Name, err)
	}
	if state == nil {
		// No state file. The process may have already exited.
		p.instance.SetAdopted(false)
		p.instance.SetStatus(Stopped)
		log.Printf("Instance %s: no runtime state found (adopted); assuming already stopped", p.instance.Name)
		return nil
	}

	log.Printf("Instance %s (adopted): stopping PID %d, port %d", p.instance.Name, state.PID, state.Port)

	// Clean stop first; hard-kill the tree only if it is refused or the
	// process outlives the grace period. Instances spawned by an older
	// binary (DETACHED_PROCESS, no console) cannot receive the request.
	exited := false
	if gerr := gracefulStopPID(state.PID); gerr != nil {
		log.Printf("Instance %s (adopted): clean stop request failed (%v); hard-killing", p.instance.Name, gerr)
	} else {
		grace := p.gracefulStopTimeout()
		log.Printf("Instance %s (adopted): clean stop requested; waiting up to %v before hard kill", p.instance.Name, grace)
		if exited = waitPIDExit(state.PID, grace); exited {
			log.Printf("Instance %s (adopted): shut down gracefully", p.instance.Name)
		}
	}

	if !exited {
		method, kerr := stopAdoptedProcessTree(p.instance.Name, state.PID)
		if kerr != nil {
			log.Printf("Instance %s (adopted): stop tree failed (%s): %v", p.instance.Name, method, kerr)
		} else {
			switch method {
			case "job", "job-assign":
				log.Printf("Instance %s (adopted): job tree terminated (%s)", p.instance.Name, method)
			case "tree-kill":
				log.Printf("Instance %s (adopted): adopted fallback tree-kill completed", p.instance.Name)
			default:
				log.Printf("Instance %s (adopted): stopped via %s", p.instance.Name, method)
			}
		}
	}

	// Wait for the port to release (up to 30s).
	deadline := time.Now().Add(30 * time.Second)
	for portInUse(host, port) {
		if time.Now().After(deadline) {
			log.Printf("Instance %s: port %d still in use after 30s; assuming stopped", p.instance.Name, port)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Clean up the runtime state file.
	if err := RemoveRuntimeState(instanceDir); err != nil {
		log.Printf("Instance %s: warning: failed to remove runtime state: %v", p.instance.Name, err)
	}

	p.instance.SetAdopted(false)
	p.instance.SetStatus(Stopped)
	log.Printf("Instance %s (adopted): stopped", p.instance.Name)
	return nil
}

// defaultGracefulStopTimeout applies when instances.graceful_stop_timeout_sec
// is unset or not positive.
const defaultGracefulStopTimeout = 30 * time.Second

// gracefulStopTimeout is how long stop waits after the clean-stop request
// before hard-killing the process tree.
func (p *process) gracefulStopTimeout() time.Duration {
	if s := p.instance.globalInstanceSettings; s != nil && s.GracefulStopTimeoutSec > 0 {
		return time.Duration(s.GracefulStopTimeoutSec) * time.Second
	}
	return defaultGracefulStopTimeout
}

func (p *process) closeChildLog() {
	if p.childLog != nil {
		_ = p.childLog.Close()
		p.childLog = nil
	}
}

func (p *process) closeJob() {
	if p.job != nil {
		p.job.close()
		p.job = nil
	}
}

func (p *process) instanceDir() string {
	return filepath.Join(p.instance.globalInstanceSettings.InstancesDir, p.instance.Name)
}

func (p *process) writeRuntimeState() {
	dir := p.instanceDir()
	gen := 1
	if prev, err := ReadRuntimeState(dir); err == nil && prev != nil {
		gen = prev.Generation + 1
	}
	pid := 0
	if p.cmd != nil && p.cmd.Process != nil {
		pid = p.cmd.Process.Pid
	}
	port := 0
	if p.instance.options != nil {
		port = p.instance.options.GetPort()
	}
	state := &RuntimeState{
		SchemaVersion: RuntimeStateSchemaVersion,
		PID:           pid,
		Port:          port,
		StartedAt:     time.Now(),
		Generation:    gen,
		LogFile:       p.instance.logger.path(),
	}
	if err := WriteRuntimeState(dir, state); err != nil {
		log.Printf("Instance %s: warning: failed to write runtime state: %v", p.instance.Name, err)
	}
}

func (p *process) removeRuntimeState() {
	if err := RemoveRuntimeState(p.instanceDir()); err != nil {
		log.Printf("Instance %s: warning: failed to remove runtime state: %v", p.instance.Name, err)
	}
}
