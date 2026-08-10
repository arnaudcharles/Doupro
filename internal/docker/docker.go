// Package docker wraps the official Docker Engine API client: container
// discovery today, with image pull/inspect, container create/stop/remove,
// and the Docker event stream to follow later (see docs/architecture.md
// and docs/containers.md).
package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerevents "github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	dockerregistry "github.com/docker/docker/api/types/registry"
	dockerclient "github.com/docker/docker/client"

	"github.com/arnaudcharles/doupro/internal/registry"
)

// labelComposeProject and labelStack mirror the label conventions
// documented in docs/containers.md for grouping containers into a stack.
const (
	labelComposeProject = "com.docker.compose.project"
	labelStack          = "doupro.stack"
	labelEnable         = "doupro.enable"
)

// Client wraps the Docker Engine API client used to observe (and, later,
// mutate) containers on the host.
type Client struct {
	cli        *dockerclient.Client
	socketPath string

	// dockerHubUsername/dockerHubPassword, when set, authenticate Pull as
	// a real Docker Hub account instead of an anonymous pull — see
	// registry.SetDockerHubCredentials for why (the same higher
	// authenticated rate limit applies to pulls, not just registry
	// digest/tag-list checks).
	dockerHubUsername string
	dockerHubPassword string
}

// New connects to the Docker daemon over the Unix socket at socketPath.
// It does not verify connectivity — call Ping or List to do that.
// dockerHubUsername/dockerHubPassword are optional (both empty means
// every Pull stays anonymous, identical to before this existed).
func New(socketPath, dockerHubUsername, dockerHubPassword string) (*Client, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHost("unix://"+socketPath),
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("create docker client for %s: %w", socketPath, err)
	}
	return &Client{cli: cli, socketPath: socketPath, dockerHubUsername: dockerHubUsername, dockerHubPassword: dockerHubPassword}, nil
}

// SocketPath is the host socket path mounted into a self-update helper.
func (c *Client) SocketPath() string { return c.socketPath }

// Close releases the underlying HTTP client's connections.
func (c *Client) Close() error {
	return c.cli.Close()
}

// Ping verifies the daemon is reachable, surfacing a clear error early
// (e.g. missing/misconfigured socket mount) instead of failing obscurely
// on the first real call.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.cli.Ping(ctx); err != nil {
		return fmt.Errorf("ping docker daemon: %w", err)
	}
	return nil
}

// Container is the subset of Docker container metadata DoUpRo tracks. See
// docs/containers.md for how State/Excluded map to the UI.
type Container struct {
	ID       string
	Name     string
	Image    string // as specified, e.g. "redis:7" — not necessarily canonical
	ImageID  string // immutable image ID, e.g. "sha256:..." — see ImageDigest
	State    string // "running", "exited", "paused", "restarting", ...
	Status   string // human-readable, e.g. "Up 3 hours"
	Stack    string
	Excluded bool
	Labels   map[string]string
}

type ContainerEvent struct {
	ContainerID string
	Name        string
	Action      string
	ExitCode    string
	Time        time.Time
	TimeNano    int64
}

// ContainerEvents subscribes to container lifecycle events from since. The
// caller must reconnect after the returned error channel yields.
func (c *Client) ContainerEvents(ctx context.Context, since time.Time) (<-chan ContainerEvent, <-chan error) {
	args := filters.NewArgs(filters.Arg("type", "container"))
	messages, errs := c.cli.Events(ctx, dockerevents.ListOptions{
		Since: since.UTC().Format(time.RFC3339Nano), Filters: args,
	})
	out := make(chan ContainerEvent)
	go func() {
		defer close(out)
		for message := range messages {
			at := time.Unix(message.Time, 0).UTC()
			if message.TimeNano > 0 {
				at = time.Unix(0, message.TimeNano).UTC()
			}
			select {
			case out <- ContainerEvent{
				ContainerID: message.Actor.ID,
				Name:        message.Actor.Attributes["name"],
				Action:      string(message.Action),
				ExitCode:    message.Actor.Attributes["exitCode"],
				Time:        at, TimeNano: message.TimeNano,
			}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, errs
}

// List returns every container on the host, running or stopped — the same
// set `docker ps -a` shows. Exclusion (doupro.enable=false) is resolved
// here from labels; name/stack exclusions configured in Settings are
// applied by the caller, which has access to those settings.
func (c *Client) List(ctx context.Context) ([]Container, error) {
	summaries, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	out := make([]Container, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, containerFromSummary(s))
	}
	return out, nil
}

// containerFromSummary maps one ContainerList entry to DoUpRo's Container
// shape — pulled out of List so the name/stack/exclusion derivation is
// testable without a Docker daemon.
func containerFromSummary(s container.Summary) Container {
	name := s.ID
	if len(s.Names) > 0 {
		name = strings.TrimPrefix(s.Names[0], "/")
	}
	return Container{
		ID:       s.ID,
		Name:     name,
		Image:    s.Image,
		ImageID:  s.ImageID,
		State:    s.State,
		Status:   s.Status,
		Stack:    StackName(s.Labels),
		Excluded: s.Labels[labelEnable] == "false",
		Labels:   s.Labels,
	}
}

// StackName resolves a container's stack from its labels — the Compose
// project label if present, else the doupro.stack override, else "" (the
// caller groups those under "Ungrouped", see docs/containers.md).
func StackName(labels map[string]string) string {
	if v := labels[labelComposeProject]; v != "" {
		return v
	}
	return labels[labelStack]
}

// ImageDigests returns every registry digest Docker has recorded for
// imageID (RepoDigests), populated whenever an image was pulled, as
// opposed to built locally. A single image can legitimately carry more
// than one digest for the same repo (e.g. after the same tag was pulled
// at different times), so callers must check *membership* against this
// set, not equality against a single value — comparing to just one entry
// produces false positives whenever the registry's current digest happens
// to be a non-first entry Docker already has locally. Returns an empty
// slice with no error if the image has no recorded digest (e.g. built
// locally, never pulled) — callers should treat that as "can't compare",
// not "no update available".
func (c *Client) ImageDigests(ctx context.Context, imageID string) ([]string, error) {
	inspect, err := c.cli.ImageInspect(ctx, imageID)
	if err != nil {
		return nil, fmt.Errorf("inspect image %s: %w", imageID, err)
	}
	digests := make([]string, 0, len(inspect.RepoDigests))
	for _, repoDigest := range inspect.RepoDigests {
		if digest, ok := parseRepoDigest(repoDigest); ok {
			digests = append(digests, digest)
		}
	}
	return digests, nil
}

// parseRepoDigest extracts the digest half of a Docker RepoDigests entry
// (e.g. "nginx@sha256:abcd..." -> "sha256:abcd..."). ok is false for an
// entry with no "@" separator, which shouldn't occur in practice but must
// not panic on if it ever does.
func parseRepoDigest(repoDigest string) (digest string, ok bool) {
	idx := strings.LastIndex(repoDigest, "@")
	if idx == -1 {
		return "", false
	}
	return repoDigest[idx+1:], true
}

// ImageVersionLabel returns the publisher-provided OCI image version for an
// immutable local image. It is intentionally read from the image ID rather
// than a mutable tag, so a newer pull cannot relabel an older running image.
func (c *Client) ImageVersionLabel(ctx context.Context, imageID, repository string) (string, error) {
	info, err := c.cli.ImageInspect(ctx, imageID)
	if err != nil {
		return "", fmt.Errorf("inspect image %s for version label: %w", imageID, err)
	}
	if info.Config == nil || info.Config.Labels == nil {
		return "", nil
	}
	return registry.VersionFromLabels(info.Config.Labels, repository), nil
}

// ImageID resolves a local image reference to its immutable ID without any
// registry traffic. Docker integration tests use it to avoid spending pull
// quota when their fixture image is already cached on the daemon.
func (c *Client) ImageID(ctx context.Context, imageRef string) (string, error) {
	inspect, err := c.cli.ImageInspect(ctx, imageRef)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", imageRef, err)
	}
	return inspect.ID, nil
}

// Inspect returns the full container configuration, used by internal/updater
// to recreate a container identically (mounts, env, networks, labels,
// restart policy) against a new image — see docs/containers.md.
func (c *Client) Inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	info, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	return info, nil
}

// pullAuth decides which credentials, if any, to send for a pull of ref:
// an explicitly configured per-registry credential (internal/registry's
// runtime config, Lot 6) takes priority; otherwise the Docker Hub
// credentials configured at startup apply, but only when ref actually
// resolves to Docker Hub — never sent to any other registry, even one
// that also happens to require Basic auth.
func pullAuth(ref registry.Ref, dockerHubUsername, dockerHubPassword string) (username, password string) {
	if cfg, ok := registry.ConfigForHost(ref.Registry); ok {
		return cfg.Username, cfg.Password
	}
	if dockerHubUsername != "" && ref.Registry == "registry-1.docker.io" {
		return dockerHubUsername, dockerHubPassword
	}
	return "", ""
}

// Pull pulls imageRef and blocks until the pull completes (or fails).
// Authenticates as a real Docker Hub account when the Client was
// configured with credentials and imageRef actually resolves to Docker
// Hub — never sends Docker Hub credentials to some other registry.
func (c *Client) Pull(ctx context.Context, imageRef string) error {
	opts := image.PullOptions{}
	ref := registry.ParseRef(imageRef)
	username, password := pullAuth(ref, c.dockerHubUsername, c.dockerHubPassword)
	if username != "" {
		encoded, err := dockerregistry.EncodeAuthConfig(dockerregistry.AuthConfig{
			Username:      username,
			Password:      password,
			ServerAddress: ref.Registry,
		})
		if err != nil {
			return fmt.Errorf("encode registry auth for %s: %w", imageRef, err)
		}
		opts.RegistryAuth = encoded
	}

	rc, err := c.cli.ImagePull(ctx, imageRef, opts)
	if err != nil {
		return fmt.Errorf("pull image %s: %w", imageRef, err)
	}
	defer rc.Close() //nolint:errcheck // stream drained fully below; nothing to recover from a close error after that
	// The pull is streamed; draining it is what makes ImagePull actually
	// wait for completion instead of returning as soon as the request
	// started.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("pull image %s: %w", imageRef, err)
	}
	return nil
}

// Stop stops a running container, giving it timeout to shut down cleanly
// before Docker kills it. A nil timeout uses Docker's default.
func (c *Client) Stop(ctx context.Context, id string, timeout *time.Duration) error {
	opts := container.StopOptions{}
	if timeout != nil {
		secs := int(timeout.Seconds())
		opts.Timeout = &secs
	}
	if err := c.cli.ContainerStop(ctx, id, opts); err != nil {
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	return nil
}

// Remove removes a (stopped) container. Force also removes a still-running
// one — used when a recreate needs to reclaim a container's name.
func (c *Client) Remove(ctx context.Context, id string, force bool) error {
	if err := c.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: force}); err != nil {
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	return nil
}

// Rename retains a stopped container as a recovery copy during self-update.
func (c *Client) Rename(ctx context.Context, id, name string) error {
	if err := c.cli.ContainerRename(ctx, id, name); err != nil {
		return fmt.Errorf("rename container %s to %s: %w", id, name, err)
	}
	return nil
}

// RemoveVolume removes a named Docker volume. Production recreation never
// calls this (volumes are user data and must survive updates); it exists for
// the disposable Docker integration harness so its anonymous fixture volume
// cannot leak after a test.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	if err := c.cli.VolumeRemove(ctx, name, true); err != nil {
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	return nil
}

// Create creates a new container from the given config, matching the
// signature internal/updater needs to recreate a container from its own
// prior inspect output with only the image swapped.
func (c *Client) Create(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig, name string) (string, error) {
	resp, err := c.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("create container %s: %w", name, err)
	}
	return resp.ID, nil
}

// Start starts a created container.
func (c *Client) Start(ctx context.Context, id string) error {
	if err := c.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container %s: %w", id, err)
	}
	return nil
}

// WaitHealthy waits for a just-started container to become ready: if the
// image defines a HEALTHCHECK, it polls until Docker reports "healthy"
// (returning an error on "unhealthy" or timeout); otherwise it applies a
// stability window — the container must still be running, without having
// restarted, once the window elapses. See docs/containers.md.
func (c *Client) WaitHealthy(ctx context.Context, id string, timeout, stabilityWindow time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	hasHealthcheck := false
	if info, err := c.Inspect(ctx, id); err == nil && info.State != nil && info.State.Health != nil {
		hasHealthcheck = true
	}

	if !hasHealthcheck {
		select {
		case <-time.After(stabilityWindow):
		case <-ctx.Done():
			return ctx.Err()
		}
		info, err := c.Inspect(ctx, id)
		if err != nil {
			return fmt.Errorf("check container %s after stability window: %w", id, err)
		}
		if info.State == nil || !info.State.Running {
			return fmt.Errorf("container %s is not running after stability window (status: %s)", id, statusOf(info))
		}
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			info, err := c.Inspect(ctx, id)
			if err != nil {
				return fmt.Errorf("poll health for container %s: %w", id, err)
			}
			if info.State != nil && info.State.Health != nil {
				switch info.State.Health.Status {
				case "healthy":
					return nil
				case "unhealthy":
					return fmt.Errorf("container %s reported unhealthy", id)
				}
			}
			if info.State == nil || !info.State.Running {
				return fmt.Errorf("container %s stopped while waiting for health (status: %s)", id, statusOf(info))
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("container %s did not become healthy within %s", id, timeout)
			}
		}
	}
}

func statusOf(info container.InspectResponse) string {
	if info.State == nil {
		return "unknown"
	}
	return info.State.Status
}
