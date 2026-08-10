package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"

	"github.com/arnaudcharles/doupro/internal/docker"
)

type dockerInspect = container.InspectResponse

type recreationResult struct {
	ID              string
	State           string
	Status          string
	originalStopped bool
	originalRemoved bool
}

// recreationPlan is built and validated before the original container is
// touched. It owns deep copies of every create-time setting returned by
// Docker inspect; callers cannot accidentally mutate the snapshot used for a
// revert.
type recreationPlan struct {
	config        *container.Config
	hostConfig    *container.HostConfig
	networkConfig *network.NetworkingConfig
	name          string
	wasRunning    bool
}

func buildRecreationPlan(before dockerInspect, targetImage string) (recreationPlan, error) {
	if before.Config == nil || before.HostConfig == nil || before.State == nil {
		return recreationPlan{}, fmt.Errorf("container inspect is missing config, host config, or state")
	}
	switch before.State.Status {
	case "running", "exited", "created":
	default:
		return recreationPlan{}, fmt.Errorf("container state %q is unsafe to recreate", before.State.Status)
	}

	cfg, err := cloneJSON(before.Config)
	if err != nil {
		return recreationPlan{}, fmt.Errorf("copy container config: %w", err)
	}
	hostCfg, err := cloneJSON(before.HostConfig)
	if err != nil {
		return recreationPlan{}, fmt.Errorf("copy host config: %w", err)
	}
	cfg.Image = targetImage
	preserveAnonymousVolumes(hostCfg, before.Mounts)

	var netCfg *network.NetworkingConfig
	if before.NetworkSettings != nil && len(before.NetworkSettings.Networks) > 0 {
		netCfg = &network.NetworkingConfig{EndpointsConfig: make(map[string]*network.EndpointSettings, len(before.NetworkSettings.Networks))}
		for name, endpoint := range before.NetworkSettings.Networks {
			if endpoint == nil {
				netCfg.EndpointsConfig[name] = nil
				continue
			}
			// EndpointSettings.Copy deliberately retains runtime IDs and IPs.
			// ContainerCreate only accepts the user-controlled subset below.
			copy := &network.EndpointSettings{
				IPAMConfig: endpoint.IPAMConfig,
				Links:      append([]string(nil), endpoint.Links...),
				Aliases:    append([]string(nil), endpoint.Aliases...),
				MacAddress: endpoint.MacAddress,
				DriverOpts: cloneStringMap(endpoint.DriverOpts),
			}
			if endpoint.IPAMConfig != nil {
				copy.IPAMConfig, err = cloneJSON(endpoint.IPAMConfig)
				if err != nil {
					return recreationPlan{}, fmt.Errorf("copy network %s IPAM config: %w", name, err)
				}
			}
			netCfg.EndpointsConfig[name] = copy
		}
	}

	return recreationPlan{
		config: cfg, hostConfig: hostCfg, networkConfig: netCfg,
		name: trimSlash(before.Name), wasRunning: before.State.Running,
	}, nil
}

func cloneJSON[T any](value *T) (*T, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Dockerfile VOLUME declarations do not appear in HostConfig.Mounts. Their
// generated volume name is only present in the top-level inspect Mounts list;
// without pinning it here, every update silently creates an empty replacement
// volume and strands the user's data.
func preserveAnonymousVolumes(hostCfg *container.HostConfig, points []container.MountPoint) {
	covered := make(map[string]struct{})
	for _, m := range hostCfg.Mounts {
		covered[m.Target] = struct{}{}
	}
	for _, bind := range hostCfg.Binds {
		parts := strings.Split(bind, ":")
		if len(parts) >= 2 {
			covered[parts[1]] = struct{}{}
		}
	}
	for target := range hostCfg.Tmpfs {
		covered[target] = struct{}{}
	}
	for _, point := range points {
		if point.Type != mount.TypeVolume || point.Name == "" {
			continue
		}
		if _, ok := covered[point.Destination]; ok {
			continue
		}
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type: mount.TypeVolume, Source: point.Name, Target: point.Destination,
			ReadOnly: !point.RW,
		})
	}
}

func (u *Updater) recreate(ctx context.Context, oldID string, before dockerInspect, targetImage string) (recreationResult, error) {
	plan, err := buildRecreationPlan(before, targetImage)
	if err != nil {
		return recreationResult{}, err
	}
	if !strings.HasPrefix(targetImage, "sha256:") {
		if err := u.docker.Pull(ctx, targetImage); err != nil {
			return recreationResult{}, fmt.Errorf("pull %s: %w", targetImage, err)
		}
	}

	result := recreationResult{}
	if oldID != "" && plan.wasRunning {
		stopTimeout := DefaultStopTimeout
		if err := u.docker.Stop(ctx, oldID, &stopTimeout); err != nil {
			return result, fmt.Errorf("stop container: %w", err)
		}
		result.originalStopped = true
	}
	if oldID != "" {
		if err := u.docker.Remove(ctx, oldID, true); err != nil {
			return result, fmt.Errorf("remove container: %w", err)
		}
		result.originalRemoved = true
	}

	newID, err := u.docker.Create(ctx, plan.config, plan.hostConfig, plan.networkConfig, plan.name)
	if err != nil {
		return result, fmt.Errorf("create container: %w", err)
	}
	result.ID = newID
	result.State = "created"
	result.Status = "Created"

	created, err := u.docker.Inspect(ctx, newID)
	if err != nil {
		return result, fmt.Errorf("inspect recreated container: %w", err)
	}
	if err := validateRecreation(plan, created); err != nil {
		return result, fmt.Errorf("recreation fidelity check: %w", err)
	}

	// Updating a stopped container must not execute its workload as a side
	// effect. It remains stopped (Docker's fresh equivalent is "created").
	if !plan.wasRunning {
		return result, nil
	}
	if err := u.docker.Start(ctx, newID); err != nil {
		return result, fmt.Errorf("start container: %w", err)
	}
	if err := u.docker.WaitHealthy(ctx, newID, DefaultHealthTimeout, DefaultStabilityWindow); err != nil {
		return result, fmt.Errorf("health check: %w", err)
	}
	created, err = u.docker.Inspect(ctx, newID)
	if err != nil {
		return result, fmt.Errorf("inspect healthy container: %w", err)
	}
	result.State = created.State.Status
	result.Status = created.State.Status
	return result, nil
}

// validateRecreation compares every create-time field supplied to Docker.
// The daemon may add defaults, so the expected JSON is checked recursively as
// a subset of the inspect response instead of requiring byte-for-byte output.
func validateRecreation(plan recreationPlan, actual dockerInspect) error {
	if actual.Config == nil || actual.HostConfig == nil {
		return fmt.Errorf("recreated container inspect is incomplete")
	}
	if err := jsonSubset(plan.config, actual.Config, "Config"); err != nil {
		return err
	}
	expectedHost, err := normalizedHostConfig(plan.hostConfig)
	if err != nil {
		return fmt.Errorf("normalize expected HostConfig: %w", err)
	}
	actualHost, err := normalizedHostConfig(actual.HostConfig)
	if err != nil {
		return fmt.Errorf("normalize actual HostConfig: %w", err)
	}
	if err := jsonSubset(expectedHost, actualHost, "HostConfig"); err != nil {
		return err
	}
	if plan.networkConfig != nil {
		if actual.NetworkSettings == nil {
			return fmt.Errorf("NetworkSettings missing")
		}
		for name, expected := range plan.networkConfig.EndpointsConfig {
			got, ok := actual.NetworkSettings.Networks[name]
			if !ok {
				return fmt.Errorf("network %q missing", name)
			}
			if expected != nil {
				if !reflect.DeepEqual(expected.IPAMConfig, got.IPAMConfig) ||
					!reflect.DeepEqual(expected.Links, got.Links) ||
					!stringSliceContained(expected.Aliases, got.Aliases) ||
					!reflect.DeepEqual(expected.DriverOpts, got.DriverOpts) {
					return fmt.Errorf("network %q user settings changed", name)
				}
				if expected.MacAddress != "" && expected.MacAddress != got.MacAddress {
					return fmt.Errorf("network %q MAC address changed", name)
				}
			}
		}
	}
	return nil
}

func normalizedHostConfig(in *container.HostConfig) (*container.HostConfig, error) {
	out, err := cloneJSON(in)
	if err != nil {
		return nil, err
	}
	// Docker inspect commonly reports an explicit pointer-to-false for an
	// inherited OOM-killer default, while ContainerCreate canonicalizes the
	// exact same setting back to nil. Both mean "do not disable the OOM
	// killer"; a true pointer remains significant and must compare exactly.
	if out.OomKillDisable != nil && !*out.OomKillDisable {
		out.OomKillDisable = nil
	}
	return out, nil
}

func jsonSubset(expected, actual any, path string) error {
	var want, got any
	wb, _ := json.Marshal(expected)
	gb, _ := json.Marshal(actual)
	if err := json.Unmarshal(wb, &want); err != nil {
		return err
	}
	if err := json.Unmarshal(gb, &got); err != nil {
		return err
	}
	return compareJSONSubset(want, got, path)
}

func compareJSONSubset(want, got any, path string) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("%s type changed", path)
		}
		for key, value := range w {
			actual, exists := g[key]
			if !exists {
				return fmt.Errorf("%s.%s missing", path, key)
			}
			if err := compareJSONSubset(value, actual, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || !reflect.DeepEqual(w, g) {
			return fmt.Errorf("%s changed", path)
		}
	default:
		if !reflect.DeepEqual(want, got) {
			return fmt.Errorf("%s changed", path)
		}
	}
	return nil
}

func stringSliceContained(want, got []string) bool {
	set := make(map[string]struct{}, len(got))
	for _, value := range got {
		set[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func trimSlash(name string) string { return strings.TrimPrefix(name, "/") }

func stackOf(labels map[string]string) string { return docker.StackName(labels) }
