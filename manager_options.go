package contkit

import "time"

type containerStartConfig struct {
	env              map[string]string
	cmd              []string
	portBinding      map[int]int
	startupDelay     time.Duration
	startupProbeCmd  []string
	startupProbeSkew time.Duration
}

type ContainerStartOption func(*containerStartConfig)

// WithEnvVariable sets an environment variable (can be used multiple times)
func WithEnvVariable(key, value string) ContainerStartOption {
	return func(c *containerStartConfig) {
		c.env[key] = value
	}
}

// WithCmd specifies the command to be used at container start
func WithCmd(cmd ...string) ContainerStartOption {
	return func(c *containerStartConfig) {
		c.cmd = cmd
	}
}

// WithPortBinding binds container port to host's port (can be used multiple times)
func WithPortBinding(host, container int) ContainerStartOption {
	return func(c *containerStartConfig) {
		c.portBinding[host] = container
	}
}

// WithStartupProbe runs the provided command inside container periodically
// until it returns zero exit code and then closes the ready channel.
//
// If used alongside startup delay, the ready channel is closed whenever
// one of the conditions finishes first.
func WithStartupProbe(cmd ...string) ContainerStartOption {
	return func(c *containerStartConfig) {
		c.startupProbeCmd = cmd
	}
}

// WithStartupProbeSkew adds an aditional delay at the end of the startup
// probe operation; total delay is therefore: startup probe + skew
//
// This has no effect on startup delay
func WithStartupProbeSkew(t time.Duration) ContainerStartOption {
	return func(c *containerStartConfig) {
		c.startupProbeSkew = t
	}
}

// WithStartupDelay delays the closing of the ready channel.
//
// If used alongside startup probe command, the ready channel is closed whenever
// one of the conditions finishes first.
func WithStartupDelay(delay time.Duration) ContainerStartOption {
	return func(c *containerStartConfig) {
		c.startupDelay = delay
	}
}
