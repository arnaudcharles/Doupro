package updater

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
)

func TestOperationGuardRejectsConcurrentOperation(t *testing.T) {
	g := newOperationGuard()
	if _, ok := g.begin("demo", "update"); !ok {
		t.Fatal("first operation was rejected")
	}
	current, ok := g.begin("demo", "rollback")
	if ok || current.Kind != "update" {
		t.Fatalf("concurrent operation = (%+v, %v), want active update and rejection", current, ok)
	}
	g.end("demo", "update")
	if _, ok := g.begin("demo", "rollback"); !ok {
		t.Fatal("operation remained locked after end")
	}
	g.end("demo", "rollback")
}

func TestOperationGuardMaintenanceIsGlobalAndRaceFree(t *testing.T) {
	g := newOperationGuard()
	if _, ok := g.begin("one", "update"); !ok {
		t.Fatal("first operation rejected")
	}
	if active, ok := g.beginMaintenance(); ok || len(active) != 1 {
		t.Fatalf("maintenance acquired over active operation: active=%v ok=%v", active, ok)
	}
	g.end("one", "update")
	if active, ok := g.beginMaintenance(); !ok || len(active) != 0 {
		t.Fatalf("maintenance not acquired: active=%v ok=%v", active, ok)
	}
	if current, ok := g.begin("two", "rollback"); ok || current.Kind != "self-update" {
		t.Fatalf("operation entered maintenance gate: current=%+v ok=%v", current, ok)
	}
	g.endMaintenance()
	if _, ok := g.begin("two", "rollback"); !ok {
		t.Fatal("gate did not reopen")
	}
	g.end("two", "rollback")
}

func TestBuildRecreationPlanPreservesConfigurationAndSnapshot(t *testing.T) {
	before := fixtureInspect("exited")
	plan, err := buildRecreationPlan(before, "example/app:2")
	if err != nil {
		t.Fatal(err)
	}
	if plan.config.Image != "example/app:2" || before.Config.Image != "example/app:1" {
		t.Fatalf("image copy mutated snapshot: plan=%q before=%q", plan.config.Image, before.Config.Image)
	}
	plan.config.Env[0] = "MODE=changed"
	plan.hostConfig.Sysctls["net.ipv4.ip_unprivileged_port_start"] = "1"
	plan.networkConfig.EndpointsConfig["testnet"].Aliases[0] = "changed"
	if before.Config.Env[0] != "MODE=production" ||
		before.HostConfig.Sysctls["net.ipv4.ip_unprivileged_port_start"] != "0" ||
		before.NetworkSettings.Networks["testnet"].Aliases[0] != "app" {
		t.Fatal("recreation plan is not independent from the inspect snapshot")
	}
	if plan.wasRunning {
		t.Fatal("stopped container plan would start the replacement")
	}
}

func TestBuildRecreationPlanPinsAnonymousVolume(t *testing.T) {
	before := fixtureInspect("running")
	plan, err := buildRecreationPlan(before, "example/app:2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.hostConfig.Mounts) != 2 {
		t.Fatalf("mount count = %d, want explicit bind plus recovered anonymous volume", len(plan.hostConfig.Mounts))
	}
	got := plan.hostConfig.Mounts[1]
	if got.Type != mount.TypeVolume || got.Source != "anonymous-volume-id" || got.Target != "/data" {
		t.Fatalf("recovered anonymous mount = %+v", got)
	}
}

func TestBuildRecreationPlanRejectsUnsafeState(t *testing.T) {
	for _, state := range []string{"paused", "restarting", "removing", "dead"} {
		t.Run(state, func(t *testing.T) {
			if _, err := buildRecreationPlan(fixtureInspect(state), "example/app:2"); err == nil {
				t.Fatalf("state %q was accepted", state)
			}
		})
	}
}

func fixtureInspect(state string) dockerInspect {
	return types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			Name: "/demo", Image: "sha256:old",
			State: &types.ContainerState{Status: state, Running: state == "running"},
			HostConfig: &container.HostConfig{
				RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
				CapDrop:       []string{"ALL"},
				Sysctls:       map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"},
				Mounts:        []mount.Mount{{Type: mount.TypeBind, Source: "/tmp/source", Target: "/config", ReadOnly: true}},
			},
		},
		Config: &container.Config{
			Image: "example/app:1", Env: []string{"MODE=production"},
			Labels: map[string]string{"doupro.test": "true"},
		},
		Mounts: []types.MountPoint{
			{Type: mount.TypeBind, Source: "/tmp/source", Destination: "/config", RW: false},
			{Type: mount.TypeVolume, Name: "anonymous-volume-id", Destination: "/data", RW: true},
		},
		NetworkSettings: &types.NetworkSettings{Networks: map[string]*network.EndpointSettings{
			"testnet": {Aliases: []string{"app"}, DriverOpts: map[string]string{"com.example.option": "value"}},
		}},
	}
}
