package verification

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// SubjectConfig describes the application under acceptance: its image, command,
// container port and health endpoint. The test container reaches it through a
// published loopback port, exposed as LOOPFORGE_SUBJECT_URL (and
// -Daegis.baseUrl) to the JUnit run.
type SubjectConfig struct {
	Docker         string
	Image          string
	Command        []string
	Port           int
	HealthPath     string
	Env            map[string]string
	StartupTimeout time.Duration
}

type Subject struct {
	ID string
	// BaseURL is host-loopback, for this process's health checks.
	BaseURL string
	// ContainerURL is how another container reaches the same port.
	ContainerURL string
	docker       string
}

// StartSubject launches the app container, publishes its port on loopback and
// waits until HealthPath answers 2xx/3xx. A container that exits during startup
// fails fast.
func StartSubject(ctx context.Context, config SubjectConfig) (*Subject, error) {
	docker := strings.TrimSpace(config.Docker)
	if docker == "" {
		docker = "docker"
	}
	image := strings.TrimSpace(config.Image)
	if image == "" {
		return nil, fmt.Errorf("subject image is required")
	}
	if config.Port <= 0 {
		return nil, fmt.Errorf("subject port is required")
	}
	healthPath := strings.TrimSpace(config.HealthPath)
	if healthPath == "" {
		healthPath = "/"
	}
	if !strings.HasPrefix(healthPath, "/") {
		return nil, fmt.Errorf("subject health path %q must start with /", healthPath)
	}
	timeout := config.StartupTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	name := fmt.Sprintf("loopforge-subject-%d", time.Now().UnixNano())
	args := []string{"run", "-d", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1::%d", config.Port),
		"-e", fmt.Sprintf("PORT=%d", config.Port),
		"-e", fmt.Sprintf("SERVER_PORT=%d", config.Port),
	}
	for key, value := range config.Env {
		if key == "" {
			continue
		}
		args = append(args, "-e", key+"="+value)
	}
	args = append(args, image)
	args = append(args, config.Command...)
	if output, err := exec.CommandContext(ctx, docker, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("start subject container: %v: %s", err, strings.TrimSpace(string(output)))
	}
	subject := &Subject{ID: strings.TrimSpace(name), docker: docker}
	cleanupOnError := func(err error) (*Subject, error) {
		subject.Stop()
		return nil, err
	}
	mapped, err := subject.hostPort(ctx, config.Port)
	if err != nil {
		return cleanupOnError(err)
	}
	subject.BaseURL = fmt.Sprintf("http://127.0.0.1:%s", mapped)
	subject.ContainerURL = fmt.Sprintf("http://host.docker.internal:%s", mapped)
	if err := subject.waitReady(ctx, subject.BaseURL+healthPath, timeout); err != nil {
		return cleanupOnError(err)
	}
	return subject, nil
}

// Stop removes the container. It is safe to call more than once.
func (s *Subject) Stop() {
	if s == nil || s.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, s.docker, "rm", "-f", s.ID).Run()
}

func (s *Subject) hostPort(ctx context.Context, port int) (string, error) {
	output, err := exec.CommandContext(ctx, s.docker, "port", s.ID, fmt.Sprintf("%d/tcp", port)).Output()
	if err != nil {
		return "", fmt.Errorf("read subject port: %w", err)
	}
	line := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	_, mapped, ok := strings.Cut(line, ":")
	if !ok || strings.TrimSpace(mapped) == "" {
		return "", fmt.Errorf("subject port output %q has no host port", line)
	}
	return strings.TrimSpace(mapped), nil
}

func (s *Subject) waitReady(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	var last string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if running, err := s.running(ctx); err == nil && !running {
			return fmt.Errorf("subject container exited before %s answered", url)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			if response, err := client.Do(request); err == nil {
				_ = response.Body.Close()
				if response.StatusCode >= 200 && response.StatusCode < 400 {
					return nil
				}
				last = response.Status
			} else {
				last = err.Error()
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("subject health %s did not become ready: %s", url, last)
}

func (s *Subject) running(ctx context.Context) (bool, error) {
	output, err := exec.CommandContext(ctx, s.docker, "inspect", "-f", "{{.State.Running}}", s.ID).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(output)) == "true", nil
}
