package contkit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type Logger interface {
	Print(v ...any)
	Printf(format string, v ...any)
}

type Manager struct {
	cli          *client.Client
	containerIDs []string
	log          Logger
}

func New(logger Logger) (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("client.NewClientWithOpts: %w", err)
	}

	return &Manager{
		cli: cli,
		log: logger,
	}, nil
}

func parsePorts(ports map[int]int) (exposed nat.PortSet, bindings map[nat.Port][]nat.PortBinding) {
	exposed = make(nat.PortSet)
	bindings = make(map[nat.Port][]nat.PortBinding)

	for hp, cp := range ports {
		hostPort := strconv.Itoa(hp)
		containerPort := strconv.Itoa(cp)

		// add to ports to expose
		exposed[nat.Port(containerPort)] = struct{}{}

		// add to port bindings map
		bindings[nat.Port(containerPort)] = []nat.PortBinding{{
			HostIP:   "0.0.0.0",
			HostPort: hostPort,
		}}
	}

	return exposed, bindings
}

func parseEnv(env map[string]string) []string {
	var ret []string

	for k, v := range env {
		ret = append(ret, fmt.Sprintf("%s=%s", k, v))
	}

	return ret
}

// displayPullProgress displays progress of docker pull
// it blocks until the pull is complete
func (m *Manager) displayPullProgress(r io.ReadCloser) {
	event := new(struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	})

	decoder := json.NewDecoder(r)
	knownLayers := make(map[string]struct{})

	for {
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				m.log.Print("\tpull complete")
				break
			}
			continue
		}

		if event.Status == "Download complete" || event.Status == "Pull complete" || event.Status == "Waiting" {
			m.log.Printf("\tlayer %q - %s", event.ID, event.Status)
		} else if event.Status == "Downloading" {
			if _, ok := knownLayers[event.ID]; !ok {
				knownLayers[event.ID] = struct{}{}
				m.log.Printf("\tlayer %q - %s", event.ID, event.Status)
			}
		}
	}
}

// StartContainer starts the specified container with provided image and options.
// It returns a "ready" channel, that is closed whenever the container is considered to be up.
// The behaviour of the readiness channel can be configured using several options.
func (m *Manager) StartContainer(image string, opts ...ContainerStartOption) (chan struct{}, error) {
	cfg := &containerStartConfig{
		env:         make(map[string]string),
		portBinding: make(map[int]int),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	ctx := context.Background()

	m.log.Printf("pulling image %q (have patience)", image)
	reader, err := m.cli.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		return nil, fmt.Errorf("m.cli.ImagePull: %w", err)
	}
	defer reader.Close()
	m.displayPullProgress(reader)

	portsExposed, portBindings := parsePorts(cfg.portBinding)

	m.log.Print("creating container")
	created, err := m.cli.ContainerCreate(
		ctx,
		&container.Config{
			Image:        image,
			Env:          parseEnv(cfg.env),
			Cmd:          cfg.cmd,
			ExposedPorts: portsExposed,
		},
		&container.HostConfig{
			PortBindings: portBindings,
		},
		nil,
		nil,
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("m.cli.ContainerCreate: %w", err)
	}

	// add to id log (used later for cleanup)
	m.containerIDs = append(m.containerIDs, created.ID)

	m.log.Print("starting container")
	err = m.cli.ContainerStart(ctx, created.ID, types.ContainerStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("m.cli.ContainerStart: %w", err)
	}

	done := make(chan struct{})

	go func() {
		// close the channel on exit
		defer close(done)
		// wait for conditions to be met
		m.waitForCondition(ctx, created.ID, cfg)
	}()

	return done, nil
}

// waitForCondition waits for either one of these conditions to be true:
//   - probe command returns 0
//   - delay expires
//   - timeout expires
func (m *Manager) waitForCondition(ctx context.Context, containerID string, cfg *containerStartConfig) {
	// use derived context for timeout and cancelation
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	atLeastOneChannel := false

	probeCh := make(chan struct{})
	if len(cfg.startupProbeCmd) > 0 {
		m.log.Print("using startup probe")
		atLeastOneChannel = true
		go func() {
			m.executeProbe(ctx, containerID, cfg.startupProbeCmd)
			probeCh <- struct{}{}
		}()
	}

	delayCh := make(chan struct{})
	if cfg.startupDelay > 0 {
		m.log.Print("using startup delay")
		atLeastOneChannel = true
		go func() {
			time.Sleep(cfg.startupDelay)
			delayCh <- struct{}{}
		}()
	}

	// no channels active, return immediately
	if !atLeastOneChannel {
		return
	}

	// wait for first condition to be true
	select {
	case <-delayCh:
		m.log.Print("startup delay done")
	case <-probeCh:
		m.log.Print("startup probe done")
		time.Sleep(cfg.startupProbeSkew)
	case <-ctx.Done():
	}
}

// executeProbe executes the probe command and waits for it to return 0
// it also returns if context is canceled
func (m *Manager) executeProbe(ctx context.Context, containerID string, probeCmd []string) {
	for {
		time.Sleep(400 * time.Millisecond)
		select {
		case <-ctx.Done():
			return
		default:
		}

		// exec command in container
		exec, err := m.cli.ContainerExecCreate(ctx, containerID, types.ExecConfig{
			Cmd:          probeCmd,
			AttachStdout: true,
			AttachStderr: true,
		})
		if err != nil {
			m.log.Printf("startup probe error: %v", err)
			continue
		}

		// wait for command to stop executing
		timeoutRead := time.After(5 * time.Second)
		for {
			time.Sleep(200 * time.Millisecond)
			select {
			case <-timeoutRead:
				break
			case <-ctx.Done():
				return
			default:
			}

			attach, err := m.cli.ContainerExecAttach(ctx, exec.ID, types.ExecStartCheck{})
			if err != nil {
				m.log.Printf("startup probe error: %v", err)
				break
			}
			scanner := bufio.NewScanner(attach.Reader)
			for scanner.Scan() {
				m.log.Printf("startup probe response:\t%s", scanner.Text())
			}

			status, err := m.cli.ContainerExecInspect(ctx, exec.ID)
			if err != nil {
				m.log.Printf("startup probe error: %v", err)
				break
			}

			// if command stopped running, return if exit code is zero,
			// try executing again otherwise
			if !status.Running {
				if status.Pid != 0 && status.ExitCode == 0 {
					return
				}
				break
			}
		}
	}
}

func (m *Manager) removeContainer(ctx context.Context, id string) error {
	err := m.cli.ContainerStop(ctx, id, container.StopOptions{})
	if err != nil {
		return fmt.Errorf("m.cli.ContainerStop: %w", err)
	}
	err = m.cli.ContainerRemove(ctx, id, types.ContainerRemoveOptions{})
	if err != nil {
		return fmt.Errorf("m.cli.ContainerRemove: %w", err)
	}

	return nil
}

func (m *Manager) Close() error {
	ctx := context.Background()

	var errs []error

	// kill and remove all containers
	for _, id := range m.containerIDs {
		errs = append(errs, m.removeContainer(ctx, id))
	}

	// close the client
	errs = append(errs, m.cli.Close())

	// join all errors into one
	return errors.Join(errs...)
}
