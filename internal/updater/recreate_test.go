package updater

import (
	"testing"

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

// TestValidateRecreationAcceptsAMatchingRecreate guards the safety check
// that runs right after Update recreates a container: it must accept a
// recreated container whose Config/HostConfig/network settings genuinely
// match what was planned, matching CLAUDE.md's "update recreates the
// container with the exact same config" guarantee.
func TestValidateRecreationAcceptsAMatchingRecreate(t *testing.T) {
	before := fixtureInspect("running")
	plan, err := buildRecreationPlan(before, "example/app:2")
	if err != nil {
		t.Fatal(err)
	}
	// A real recreated container's inspect reflects exactly what
	// ContainerCreate was given, i.e. the plan itself (including the
	// anonymous-volume mount preserveAnonymousVolumes recovered into
	// plan.hostConfig.Mounts) — so the accepted case is built from the
	// plan, not a second independent fixture that would drift from it.
	actual := fixtureInspect("running")
	actual.Config = plan.config
	actual.HostConfig = plan.hostConfig
	if err := validateRecreation(plan, actual); err != nil {
		t.Fatalf("validateRecreation rejected a matching recreate: %v", err)
	}
}

// TestValidateRecreationCatchesDroppedSettings is the negative case: if the
// recreated container silently lost a mount, an env var, or a network alias
// (e.g. a Docker Engine bug, or a future refactor of buildRecreationPlan
// that stops copying something), validateRecreation must reject the
// recreate rather than let it through — this is the last line of defense
// against silently orphaning a volume or losing an env var.
func TestValidateRecreationCatchesDroppedSettings(t *testing.T) {
	t.Run("dropped env var", func(t *testing.T) {
		before := fixtureInspect("running")
		plan, err := buildRecreationPlan(before, "example/app:2")
		if err != nil {
			t.Fatal(err)
		}
		actual := fixtureInspect("running")
		actual.Config.Image = "example/app:2"
		actual.Config.Env = nil
		if err := validateRecreation(plan, actual); err == nil {
			t.Fatal("validateRecreation accepted a recreate that dropped Config.Env")
		}
	})

	t.Run("dropped bind mount", func(t *testing.T) {
		before := fixtureInspect("running")
		plan, err := buildRecreationPlan(before, "example/app:2")
		if err != nil {
			t.Fatal(err)
		}
		actual := fixtureInspect("running")
		actual.Config.Image = "example/app:2"
		actual.HostConfig.Mounts = nil
		if err := validateRecreation(plan, actual); err == nil {
			t.Fatal("validateRecreation accepted a recreate that dropped HostConfig.Mounts")
		}
	})

	t.Run("dropped network", func(t *testing.T) {
		before := fixtureInspect("running")
		plan, err := buildRecreationPlan(before, "example/app:2")
		if err != nil {
			t.Fatal(err)
		}
		actual := fixtureInspect("running")
		actual.Config.Image = "example/app:2"
		actual.NetworkSettings.Networks = map[string]*network.EndpointSettings{}
		if err := validateRecreation(plan, actual); err == nil {
			t.Fatal("validateRecreation accepted a recreate that dropped the testnet network")
		}
	})

	t.Run("changed network alias", func(t *testing.T) {
		before := fixtureInspect("running")
		plan, err := buildRecreationPlan(before, "example/app:2")
		if err != nil {
			t.Fatal(err)
		}
		actual := fixtureInspect("running")
		actual.Config.Image = "example/app:2"
		actual.NetworkSettings.Networks["testnet"].Aliases = []string{"renamed"}
		if err := validateRecreation(plan, actual); err == nil {
			t.Fatal("validateRecreation accepted a recreate with a changed network alias")
		}
	})

	t.Run("incomplete inspect", func(t *testing.T) {
		before := fixtureInspect("running")
		plan, err := buildRecreationPlan(before, "example/app:2")
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRecreation(plan, container.InspectResponse{}); err == nil {
			t.Fatal("validateRecreation accepted an inspect response with a nil Config/HostConfig")
		}
	})
}

func TestStringSliceContained(t *testing.T) {
	cases := []struct {
		name       string
		want, have []string
		contained  bool
	}{
		{"exact match", []string{"a", "b"}, []string{"a", "b"}, true},
		{"superset actual", []string{"a"}, []string{"a", "b"}, true},
		{"missing element", []string{"a", "b"}, []string{"a"}, false},
		{"empty want", nil, []string{"a"}, true},
		{"empty both", nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stringSliceContained(tc.want, tc.have); got != tc.contained {
				t.Fatalf("stringSliceContained(%v, %v) = %v, want %v", tc.want, tc.have, got, tc.contained)
			}
		})
	}
}

// TestNormalizedHostConfigTreatsExplicitFalseOOMKillDisableAsDefault guards
// the specific Docker Engine quirk normalizedHostConfig exists to work
// around (see its comment): ContainerInspect on the pre-update container
// can report OomKillDisable as an explicit *false, while ContainerCreate
// canonicalizes the same "do not disable" default back to nil. Without this
// normalization, validateRecreation would spuriously reject every
// otherwise-correct recreate whose original container happened to have this
// field set.
func TestNormalizedHostConfigTreatsExplicitFalseOOMKillDisableAsDefault(t *testing.T) {
	explicitFalse := false
	in := &container.HostConfig{Resources: container.Resources{OomKillDisable: &explicitFalse}}
	out, err := normalizedHostConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.OomKillDisable != nil {
		t.Fatalf("OomKillDisable = %v, want nil after normalization", out.OomKillDisable)
	}

	explicitTrue := true
	in2 := &container.HostConfig{Resources: container.Resources{OomKillDisable: &explicitTrue}}
	out2, err := normalizedHostConfig(in2)
	if err != nil {
		t.Fatal(err)
	}
	if out2.OomKillDisable == nil || !*out2.OomKillDisable {
		t.Fatal("an explicit true OomKillDisable must not be normalized away")
	}
}

func fixtureInspect(state string) dockerInspect {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			Name: "/demo", Image: "sha256:old",
			State: &container.State{Status: container.ContainerState(state), Running: state == "running"},
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
		Mounts: []container.MountPoint{
			{Type: mount.TypeBind, Source: "/tmp/source", Destination: "/config", RW: false},
			{Type: mount.TypeVolume, Name: "anonymous-volume-id", Destination: "/data", RW: true},
		},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
			"testnet": {Aliases: []string{"app"}, DriverOpts: map[string]string{"com.example.option": "value"}},
		}},
	}
}
