// Package docker wraps the docker CLI for container lifecycle. Using the CLI
// (not the SDK) keeps wings dependency-free and statically linked; the CLI
// talks to the daemon over the same socket the SDK would use. Server identity
// is the panel server UUID, container name is kg-<serverID>.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
func EnsureExists(ctx context.Context, hostDataDir, serverID, image string, env map[string]string, ports []string, memoryMB, cpuPercent int, startup, network string) (string, error) {
	name := ContainerName(serverID)
	inspectArgs := []string{"inspect", "-f", "{{.Id}}", name}
	if _, err := run(ctx, inspectArgs...); err == nil {
		return name, nil // already created
	}
	filesDir := filepath.Join(hostDataDir, "servers", serverID, "files")
	args := []string{"create", "--name", name, "--restart", "unless-stopped", "-w", "/server", "-v", filesDir + ":/server"}
	if network != "" {
		args = append(args, "--network", network)
	}
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
func Recreate(ctx context.Context, hostDataDir, serverID, image string, env map[string]string, ports []string, memoryMB, cpuPercent int, startup, network string) error {
	name := ContainerName(serverID)
	wasRunning := false
	if st, err := run(ctx, "inspect", "-f", "{{.State.Running}}", name); err == nil && st == "true" {
		wasRunning = true
	}
	if _, err := run(ctx, "rm", "-f", name); err != nil && !strings.Contains(err.Error(), "No such") {
		return err
	}
	if _, err := EnsureExists(ctx, hostDataDir, serverID, image, env, ports, memoryMB, cpuPercent, startup, network); err != nil {
		return err
	}
	if wasRunning {
		if _, err := run(ctx, "start", name); err != nil {
			return err
		}
		// a freshly recreated container is "created" until docker actually
		// mounts/attaches — wait briefly so the caller sees it running.
		for i := 0; i < 15; i++ {
			if st, err := run(ctx, "inspect", "-f", "{{.State.Running}}", name); err == nil && st == "true" {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return nil
}

// Remove force-removes the server's container (if present). Used by reinstall
// to get a clean slate before re-creating from the egg spec.
func Remove(ctx context.Context, serverID string) error {
	name := ContainerName(serverID)
	if _, err := run(ctx, "rm", "-f", name); err != nil {
		return err
	}
	return nil
}

// TagImage tags an existing image (docker tag src dst). Used to snapshot the
// running panel image as kgpanel:previous before an update so rollback can
// recreate the container from it.
func TagImage(ctx context.Context, src, dst string) error {
	if _, err := run(ctx, "tag", src, dst); err != nil {
		return err
	}
	return nil
}

// ImageExists reports whether the local daemon has the named image.
func ImageExists(ctx context.Context, image string) bool {
	_, err := run(ctx, "image", "inspect", image)
	return err == nil
}

// BuildImage builds from a local context dir with the given Dockerfile-relative
// path, passing VERSION as a build arg. contextDir must be visible to the
// docker daemon (i.e. under hostDataDir when wings runs in a container).
func BuildImage(ctx context.Context, contextDir, dockerfileRel, image, version string) error {
	args := []string{"build", "--build-arg", "VERSION=" + version, "-f", filepath.Join(contextDir, dockerfileRel), "-t", image, contextDir}
	if _, err := run(ctx, args...); err != nil {
		return err
	}
	return nil
}

// PanelImage returns the image ref a container was created from
// (.Config.Image from docker inspect). Used to know what to tag as -previous
// and what the rollback target should be.
func PanelImage(ctx context.Context, container string) (string, error) {
	out, err := run(ctx, "inspect", "-f", "{{.Config.Image}}", container)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// PanelInspect captures the bits docker run needs to recreate a container with
// the same settings: env (all of it — including the panel secret, so it is
// preserved across an update without the secret ever being parsed), bind
// mounts, published ports and restart policy.
func PanelInspect(ctx context.Context, container string) (Env []string, MountArgs, PortArgs []string, Restart string, RetryCount int, NetworkMode string, err error) {
	out, err := run(ctx, "inspect", container)
	if err != nil {
		return nil, nil, nil, "", 0, "", err
	}
	var ins []struct {
		Config struct {
			Env []string `json:"Env"`
		} `json:"Config"`
		HostConfig struct {
			PortBindings map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
			RestartPolicy struct {
				Name              string `json:"Name"`
				MaximumRetryCount int    `json:"MaximumRetryCount"`
			} `json:"RestartPolicy"`
			NetworkMode string `json:"NetworkMode"`
		} `json:"HostConfig"`
		Mounts []struct {
			Type        string `json:"Type"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			RW          bool   `json:"RW"`
		} `json:"Mounts"`
	}
	if err := json.Unmarshal([]byte(out), &ins); err != nil || len(ins) == 0 {
		return nil, nil, nil, "", 0, "", fmt.Errorf("bad docker inspect for %s: %w", container, err)
	}
	c := ins[0]
	// only explicit (non-created-by-docker) env is needed; docker injects
	// PATH/HOSTNAME itself. Keep everything else so secrets+config survive.
	skip := map[string]bool{"PATH": true, "HOSTNAME": true, "HOME": true}
	for _, e := range c.Config.Env {
		k := strings.SplitN(e, "=", 2)[0]
		if !skip[k] {
			Env = append(Env, e)
		}
	}
	for _, m := range c.Mounts {
		if m.Type != "bind" || m.Source == "" {
			continue
		}
		arg := m.Source + ":" + m.Destination
		if !m.RW {
			arg += ":ro"
		}
		MountArgs = append(MountArgs, arg)
	}
	for cport, binds := range c.HostConfig.PortBindings {
		for _, b := range binds {
			hp := b.HostPort
			if b.HostIP != "" && b.HostIP != "0.0.0.0" && b.HostIP != "::" {
				hp = b.HostIP + ":" + hp
			}
			arg := hp + ":" + cport
			PortArgs = append(PortArgs, arg)
		}
	}
	return Env, MountArgs, PortArgs, c.HostConfig.RestartPolicy.Name, c.HostConfig.RestartPolicy.MaximumRetryCount, c.HostConfig.NetworkMode, nil
}

// RecreatePanelContainer swaps the panel container to the given image,
// preserving env, mounts, ports, network and restart policy from the CURRENT
// container. The old container is removed first (docker rm -f), then a fresh
// one is created and started with the same settings — the container recreation
// is what makes the new image actually take effect.
func RecreatePanelContainer(ctx context.Context, container, image string) error {
	env, mounts, ports, restart, retries, netmode, err := PanelInspect(ctx, container)
	if err != nil {
		return err
	}
	if _, err := run(ctx, "rm", "-f", container); err != nil {
		return err
	}
	args := []string{"run", "-d", "--name", container}
	if restart != "" {
		r := "--restart=" + restart
		if restart == "on-failure" && retries > 0 {
			r = fmt.Sprintf("--restart=on-failure:%d", retries)
		}
		args = append(args, r)
	}
	if netmode != "" && netmode != "default" {
		args = append(args, "--network", netmode)
	}
	for _, m := range mounts {
		args = append(args, "-v", m)
	}
	for _, p := range ports {
		args = append(args, "-p", p)
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, image)
	if _, err := run(ctx, args...); err != nil {
		return err
	}
	return nil
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
