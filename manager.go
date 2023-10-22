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

type ContainterManager struct {
	cli          *client.Client
	containerIDs []string
	log          Logger
}

func NewContainerManager(logger Logger) (*ContainterManager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("client.NewClientWithOpts: %w", err)
	}

	return &ContainterManager{
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
func (cm *ContainterManager) displayPullProgress(r io.ReadCloser) {
	event := new(struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	})

	decoder := json.NewDecoder(r)
	knownLayers := make(map[string]struct{})

	for {
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				cm.log.Print("\tpull complete")
				break
			}
			continue
		}

		if event.Status == "Download complete" || event.Status == "Pull complete" || event.Status == "Waiting" {
			cm.log.Printf("\tlayer %q - %s", event.ID, event.Status)
		} else if event.Status == "Downloading" {
			if _, ok := knownLayers[event.ID]; !ok {
				knownLayers[event.ID] = struct{}{}
				cm.log.Printf("\tlayer %q - %s", event.ID, event.Status)
			}
		}
	}
}

// StartContainer starts the specified container with provided image and options.
// It returns a "ready" channel, that is closed whenever the container is considered to be up.
// The behaviour of the readiness channel can be configured using several options.
func (cm *ContainterManager) StartContainer(image string, opts ...ContainerStartOption) (chan struct{}, error) {
	cfg := &containerStartConfig{
		env:         make(map[string]string),
		portBinding: make(map[int]int),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	ctx := context.Background()

	cm.log.Printf("pulling image %q (have patience)", image)
	reader, err := cm.cli.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		return nil, fmt.Errorf("cm.cli.ImagePull: %w", err)
	}
	defer reader.Close()
	cm.displayPullProgress(reader)

	portsExposed, portBindings := parsePorts(cfg.portBinding)

	cm.log.Print("creating container")
	created, err := cm.cli.ContainerCreate(
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
		return nil, fmt.Errorf("cm.cli.ContainerCreate: %w", err)
	}

	// add to id log (used later for cleanup)
	cm.containerIDs = append(cm.containerIDs, created.ID)

	cm.log.Print("starting container")
	err = cm.cli.ContainerStart(ctx, created.ID, types.ContainerStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("cm.cli.ContainerStart: %w", err)
	}

	done := make(chan struct{})

	go func() {
		// close the channel on exit
		defer close(done)
		// wait for conditions to be met
		cm.waitForCondition(ctx, created.ID, cfg)
	}()

	return done, nil
}

// waitForCondition waits for either one of these conditions to be true:
//   - probe command returns 0
//   - delay expires
//   - timeout expires
func (cm *ContainterManager) waitForCondition(ctx context.Context, containerID string, cfg *containerStartConfig) {
	// use derived context for timeout and cancelation
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	atLeastOneChannel := false

	probeCh := make(chan struct{})
	if len(cfg.startupProbeCmd) > 0 {
		cm.log.Print("using startup probe")
		atLeastOneChannel = true
		go func() {
			cm.executeProbe(ctx, containerID, cfg.startupProbeCmd)
			probeCh <- struct{}{}
		}()
	}

	delayCh := make(chan struct{})
	if cfg.startupDelay > 0 {
		cm.log.Print("using startup delay")
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
		cm.log.Print("startup delay done")
	case <-probeCh:
		cm.log.Print("startup probe done")
		time.Sleep(cfg.startupProbeSkew)
	case <-ctx.Done():
	}
}

// executeProbe executes the probe command and waits for it to return 0
// it also returns if context is canceled
func (cm *ContainterManager) executeProbe(ctx context.Context, containerID string, probeCmd []string) {
	for {
		time.Sleep(400 * time.Millisecond)
		select {
		case <-ctx.Done():
			return
		default:
		}

		// exec command in container
		exec, err := cm.cli.ContainerExecCreate(ctx, containerID, types.ExecConfig{
			Cmd:          probeCmd,
			AttachStdout: true,
			AttachStderr: true,
		})
		if err != nil {
			cm.log.Printf("startup probe error: %v", err)
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

			attach, err := cm.cli.ContainerExecAttach(ctx, exec.ID, types.ExecStartCheck{})
			if err != nil {
				cm.log.Printf("startup probe error: %v", err)
				break
			}
			scanner := bufio.NewScanner(attach.Reader)
			for scanner.Scan() {
				cm.log.Printf("startup probe response:\t%s", scanner.Text())
			}

			status, err := cm.cli.ContainerExecInspect(ctx, exec.ID)
			if err != nil {
				cm.log.Printf("startup probe error: %v", err)
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

func (cm *ContainterManager) removeContainer(ctx context.Context, id string) error {
	err := cm.cli.ContainerStop(ctx, id, container.StopOptions{})
	if err != nil {
		return fmt.Errorf("cm.cli.ContainerStop: %w", err)
	}
	err = cm.cli.ContainerRemove(ctx, id, types.ContainerRemoveOptions{})
	if err != nil {
		return fmt.Errorf("cm.cli.ContainerRemove: %w", err)
	}

	return nil
}

func (cm *ContainterManager) Close() error {
	ctx := context.Background()

	var errs []error

	// kill and remove all containers
	for _, id := range cm.containerIDs {
		errs = append(errs, cm.removeContainer(ctx, id))
	}

	// close the client
	errs = append(errs, cm.cli.Close())

	// join all errors into one
	return errors.Join(errs...)
}
