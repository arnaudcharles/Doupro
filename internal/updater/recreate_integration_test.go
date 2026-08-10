//go:build integration

package updater

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	douprodocker "github.com/arnaudcharles/doupro/internal/docker"
)

func TestDockerRecreatePreservesStoppedStateAndAnonymousVolume(t *testing.T) {
	if os.Getenv("DOUPRO_DOCKER_INTEGRATION") != "1" {
		t.Skip("set DOUPRO_DOCKER_INTEGRATION=1 to run disposable Docker tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	socket := os.Getenv("DOUPRO_TEST_DOCKER_SOCKET")
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	client, err := douprodocker.New(socket, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	image := os.Getenv("DOUPRO_TEST_IMAGE")
	if image == "" {
		image = "busybox:1.36"
	}
	if _, err := client.ImageID(ctx, image); err != nil {
		if err := client.Pull(ctx, image); err != nil {
			t.Fatalf("fixture image %s is not local and could not be pulled: %v", image, err)
		}
	}
	name := "doupro-it-fidelity-" + strings.ToLower(time.Now().UTC().Format("150405.000000000"))
	name = strings.ReplaceAll(name, ".", "-")
	var disposableVolume string
	id, err := client.Create(ctx, &container.Config{
		Image:      image,
		Cmd:        []string{"sh", "-c", "test -f /data/marker || echo retained >/data/marker"},
		Env:        []string{"DOUPRO_FIDELITY=expected"},
		Labels:     map[string]string{"doupro.test": "true", "doupro.enable": "false", "doupro.test.kind": "recreation-fidelity"},
		WorkingDir: "/data",
		Volumes:    map[string]struct{}{"/data": {}},
	}, &container.HostConfig{
		RestartPolicy:  container.RestartPolicy{Name: "no"},
		CapDrop:        []string{"NET_RAW"},
		ReadonlyRootfs: false,
	}, nil, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Name remains stable even when recreate has removed oldID and then
		// fails before returning the replacement ID.
		_ = client.Remove(context.Background(), name, true)
		if disposableVolume != "" {
			_ = client.RemoveVolume(context.Background(), disposableVolume)
		}
	})
	if err := client.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitForStopped(t, ctx, client, id)
	before, err := client.Inspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Mounts) != 1 || before.Mounts[0].Name == "" {
		t.Fatalf("fixture did not create an anonymous volume: %+v", before.Mounts)
	}
	volumeName := before.Mounts[0].Name
	disposableVolume = volumeName

	u := &Updater{docker: client}
	result, err := u.recreate(ctx, id, before, before.Image)
	if err != nil {
		t.Fatal(err)
	}
	after, err := client.Inspect(ctx, result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State.Running || after.State.Status != "created" {
		t.Fatalf("stopped workload was started: state=%s running=%v", after.State.Status, after.State.Running)
	}
	if len(after.Mounts) != 1 || after.Mounts[0].Name != volumeName {
		t.Fatalf("anonymous volume changed: before=%q after=%+v", volumeName, after.Mounts)
	}
	if after.Config.Env[0] != "DOUPRO_FIDELITY=expected" || after.Config.Labels["doupro.test"] != "true" {
		t.Fatalf("container config changed: env=%v labels=%v", after.Config.Env, after.Config.Labels)
	}
}

func waitForStopped(t *testing.T, ctx context.Context, client *douprodocker.Client, id string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			info, err := client.Inspect(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if info.State != nil && !info.State.Running && info.State.Status == "exited" {
				return
			}
		}
	}
}
