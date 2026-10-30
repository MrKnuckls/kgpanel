// Package docker wraps the docker CLI for container lifecycle. Using the CLI
// (not the SDK) keeps wings dependency-free and statically linked; the CLI
// talks to the daemon over the same socket the SDK would use. Server identity
// is the panel server UUID, container name is kg-<serverID>.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func ContainerName(serverID string) string {
	return "kg-" + serverID
}

// EnsureExists creates the container if it is missing; idempotent.
// The server's files dir (hostDataDir/servers/<serverID>/files) is mounted at
// /server and used as the workdir, so egg startups like "python main.py" see
// the files managed through the panel file manager. hostDataDir must be the
// path on the docker HOST (== dataDir for bare-metal wings; different when
// wings itself runs in a container — see KGWINGS_HOST_DATA).
func EnsureExists(ctx context.Context, hostDataDir, serverID, image string, env map[string]string, ports []string, memoryMB, cpuPercent int, startup string) (string, error) {
	name := ContainerName(serverID)
	inspectArgs := []string{"inspect", "-f", "{{.Id}}", name}
	if _, err := run(ctx, inspectArgs...); err == nil {
		return name, nil // already created
	}
	filesDir := filepath.Join(hostDataDir, "servers", serverID, "files")
	args := []string{"create", "--name", name, "--restart", "unless-stopped", "-w", "/server", "-v", filesDir + ":/server"}
	if memoryMB > 0 {
		args = append(args, "-m", fmt.Sprintf("%dm", memoryMB))
	}
	if cpuPercent > 0 {
		// docker --cpu-quota is in 100us units: 100% == 1 core == 100000
		quota := cpuPercent * 1000
		args = append(args, "--cpu-quota", fmt.Sprintf("%d", quota))
	}
	for k, v := range env {
		if v == "" {
			continue
		}
		args = append(args, "-e", k+"="+v)
	}
	for _, p := range ports {
		if p == "" {
			continue
		}
		args = append(args, "-p", p)
	}
	args = append(args, image)
	if startup != "" {
		// PID-1-in-a-container signal forwarding: Linux ignores signals with
		// default action when the target is PID 1, so a bare `python main.py`
		// as PID 1 swallows SIGTERM and docker stop must wait its full timeout
		// before force-killing (SIGKILL). Keep sh as PID 1 with a TERM trap
		// that forwards the signal to the server process — a non-PID-1 process
		// with default disposition dies immediately, so graceful stops are
		// fast and cooperative servers still get their chance to shut down.
		// The subshell ensures $! is the real process (dash execs a simple
		// single-command subshell, so the trap kills the actual server).
		wrapper := "trap 'kill -TERM $child 2>/dev/null' TERM INT; ( " + startup + " ) & child=$!; wait $child"
		args = append(args, "/bin/sh", "-c", wrapper)
	}
	if _, err := run(ctx, args...); err != nil {
		return "", err
	}
	return name, nil
}

// Recreate removes the container if present, then recreates it from the given
// spec. Used when server settings (ports, limits, image, startup) change —
// those are baked in at create time, so a running container must be rebuilt.
// The server's files dir bind-mount survives (it is outside the container).
func Recreate(ctx context.Context, hostDataDir, serverID, image string, env map[string]string, ports []string, memoryMB, cpuPercent int, startup string) error {
	name := ContainerName(serverID)
	if _, err := run(ctx, "rm", "-f", name); err != nil && !strings.Contains(err.Error(), "No such") {
		return err
	}
	_, err := EnsureExists(ctx, hostDataDir, serverID, image, env, ports, memoryMB, cpuPercent, startup)
	return err
}

// Power maps a panel signal to docker.
func Power(ctx context.Context, serverID, signal string, stopTimeout int) error {
	name := ContainerName(serverID)
	var args []string
	switch signal {
	case "start":
		args = []string{"start", name}
	case "stop":
		args = []string{"stop", "-t", fmt.Sprintf("%d", stopTimeout), name}
	case "restart":
		args = []string{"restart", "-t", fmt.Sprintf("%d", stopTimeout), name}
	case "kill":
		args = []string{"kill", name}
	default:
		return fmt.Errorf("unknown signal %q", signal)
	}
	_, err := run(ctx, args...)
	return err
}

// Status returns the container's running state: running|stopped|starting|missing.
func Status(ctx context.Context, serverID string) (string, error) {
	name := ContainerName(serverID)
	out, err := run(ctx, "inspect", "-f", "{{.State.Status}}", name)
	if err != nil {
		if strings.Contains(err.Error(), "No such") || strings.Contains(err.Error(), "no such") {
			return "missing", nil
		}
		return "unknown", err
	}
	status := out
	switch status {
	case "running":
		// running but not yet healthy isn't distinguishable without healthchecks;
		// a freshly started container is "running" the moment docker says so.
		return "running", nil
	case "exited", "created", "dead", "paused":
		return "stopped", nil
	default:
		return "starting", nil
	}
}

// ConsoleLines tails the container log, streaming to the given handler line by
// line until ctx is done. Handler receives []byte lines (newline-stripped).
func ConsoleLines(ctx context.Context, serverID string, handler func([]byte)) error {
	name := ContainerName(serverID)
	cmd := exec.CommandContext(ctx, "docker", "logs", "--follow", "--tail", "200", "-t", name)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = cmd.Process.Kill()
	}()
	defer cmd.Wait()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		handler(append([]byte(nil), sc.Bytes()...))
	}
	return sc.Err()
}

// ExecCommand runs a command inside the container (console input channel).
func ExecCommand(ctx context.Context, serverID string, stdin bytes.Buffer) (string, error) {
	name := ContainerName(serverID)
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", name, "/bin/sh", "-c", "cat")
	cmd.Stdin = &stdin
	out, err := cmd.CombinedOutput()
	return string(out), err
}
