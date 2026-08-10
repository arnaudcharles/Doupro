package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/strslice"

	"github.com/arnaudcharles/doupro/internal/docker"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/metrics"
	"github.com/arnaudcharles/doupro/internal/store"
)

const SelfUpdateSafetyWindow = 5 * time.Minute

// SelfUpdateSafetyError means the global maintenance gate or schedule
// horizon prevents a safe daemon replacement.
type SelfUpdateSafetyError struct{ Reason string }

func (e *SelfUpdateSafetyError) Error() string { return e.Reason }

// IsSelf identifies this daemon's container independently of its name. Docker
// normally sets hostname to the container ID; the stable label is the fallback
// for deployments that override hostname.
func (u *Updater) IsSelf(ctx context.Context, containerID string) bool {
	selfID, err := u.selfContainerID(ctx)
	return err == nil && strings.HasPrefix(selfID, containerID) || err == nil && strings.HasPrefix(containerID, selfID)
}

func (u *Updater) selfContainerID(ctx context.Context) (string, error) {
	items, err := u.docker.List(ctx)
	if err != nil {
		return "", err
	}
	hostname, _ := os.Hostname()
	for _, item := range items {
		if hostname != "" && strings.HasPrefix(item.ID, hostname) {
			return item.ID, nil
		}
	}
	var labelled []string
	for _, item := range items {
		if item.Labels["doupro.instance"] == "true" {
			labelled = append(labelled, item.ID)
		}
	}
	if len(labelled) != 1 {
		return "", fmt.Errorf("self-update requires exactly one doupro.instance=true container (found %d)", len(labelled))
	}
	return labelled[0], nil
}

// StartSelfUpdate atomically closes the global mutation gate, checks the
// five-minute schedule horizon, pulls the target, and starts a sibling helper.
// The helper owns the stop/rename/recreate/rollback sequence after this daemon
// returns 202 to its caller.
func (u *Updater) StartSelfUpdate(ctx context.Context, containerID, targetImage string, actorID string) error {
	metricStatus := "failed"
	defer func() { metrics.SelfUpdatesTotal.WithLabelValues(metricStatus).Inc() }()
	active, ok := u.operations.beginMaintenance()
	if !ok {
		metricStatus = "refused"
		return &SelfUpdateSafetyError{Reason: fmt.Sprintf("self-update refused: active operations: %s", strings.Join(active, ", "))}
	}
	release := true
	defer func() {
		if release {
			u.operations.endMaintenance()
		}
	}()

	selfID, err := u.selfContainerID(ctx)
	if err != nil || (!strings.HasPrefix(selfID, containerID) && !strings.HasPrefix(containerID, selfID)) {
		metricStatus = "refused"
		return &SelfUpdateSafetyError{Reason: "self-update identity could not be verified"}
	}
	if u.selfBlockers == nil || u.shutdown == nil {
		return errors.New("self-update is not configured")
	}
	blockers, err := u.selfBlockers(ctx, time.Now().UTC().Add(SelfUpdateSafetyWindow))
	if err != nil {
		return fmt.Errorf("check self-update safety window: %w", err)
	}
	if len(blockers) != 0 {
		metricStatus = "refused"
		return &SelfUpdateSafetyError{Reason: "self-update refused: " + strings.Join(blockers, "; ")}
	}
	if err := u.docker.Pull(ctx, targetImage); err != nil {
		return fmt.Errorf("prepare self-update image: %w", err)
	}

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	helperName := "doupro-self-update-" + token
	dbPath := envOrDefault(parentEnv(ctx, u.docker, selfID), "DOUPRO_DB_PATH", "/data/doupro.db")
	cfg := &container.Config{
		Image:  targetImage,
		Cmd:    strslice.StrSlice{"self-update-helper", "--parent-id", selfID, "--target-image", targetImage, "--token", token, "--socket", u.docker.SocketPath(), "--db-path", dbPath},
		Labels: map[string]string{"doupro.enable": "false", "doupro.self-update-helper": "true"},
	}
	parent, err := u.docker.Inspect(ctx, selfID)
	if err != nil {
		return fmt.Errorf("inspect self-update parent: %w", err)
	}
	hostCfg := &container.HostConfig{AutoRemove: true, Binds: []string{u.docker.SocketPath() + ":" + u.docker.SocketPath()}}
	if parent.HostConfig != nil {
		hostCfg.GroupAdd = append([]string(nil), parent.HostConfig.GroupAdd...)
	}
	for _, point := range parent.Mounts {
		if dbPath != point.Destination && !strings.HasPrefix(dbPath, strings.TrimSuffix(point.Destination, "/")+"/") {
			continue
		}
		source := point.Source
		if point.Type == mount.TypeVolume {
			source = point.Name
		}
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{Type: point.Type, Source: source, Target: point.Destination, ReadOnly: !point.RW})
		break
	}
	helperID, err := u.docker.Create(ctx, cfg, hostCfg, nil, helperName)
	if err != nil {
		return fmt.Errorf("create self-update helper: %w", err)
	}
	if err := u.docker.Start(ctx, helperID); err != nil {
		_ = u.docker.Remove(context.Background(), helperID, true)
		return fmt.Errorf("start self-update helper: %w", err)
	}
	u.logger.Emit(ctx, events.Event{Level: events.LevelInfo, Type: "self_update.started", Container: trimSlash(mustInspectName(ctx, u.docker, selfID)), Actor: events.ActorUser, ActorID: actorID, ToVersion: targetImage, Message: "safe self-update helper started"})
	release = false // process shutdown releases the in-memory gate.
	metricStatus = "accepted"
	go func() {
		time.Sleep(time.Second)
		u.shutdown()
	}()
	return nil
}

func mustInspectName(ctx context.Context, cli *docker.Client, id string) string {
	info, err := cli.Inspect(ctx, id)
	if err != nil {
		return id
	}
	return info.Name
}

func parentEnv(ctx context.Context, cli *docker.Client, id string) []string {
	info, err := cli.Inspect(ctx, id)
	if err != nil || info.Config == nil {
		return nil
	}
	return info.Config.Env
}

func envOrDefault(env []string, key, fallback string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return fallback
}

// RunSelfUpdateHelper is the hidden helper command executed from the target
// image. The original container remains intact under a recovery name until the
// replacement is healthy.
func RunSelfUpdateHelper(ctx context.Context, socket, dbPath, parentID, targetImage, token string) error {
	cli, err := docker.New(socket, "", "")
	if err != nil {
		return err
	}
	defer cli.Close() //nolint:errcheck // best-effort on helper exit
	before, err := cli.Inspect(ctx, parentID)
	if err != nil {
		return fmt.Errorf("inspect self-update parent: %w", err)
	}
	plan, err := buildRecreationPlan(before, targetImage)
	if err != nil {
		return err
	}
	// Docker's generated hostname is the old short container ID. Do not
	// preserve it: the replacement must receive its own ID-based hostname so
	// it can identify itself even while the recovery container still exists.
	if plan.config.Hostname != "" && strings.HasPrefix(parentID, plan.config.Hostname) {
		plan.config.Hostname = ""
	}
	recoveryName := plan.name + "-doupro-recovery-" + token
	stopTimeout := DefaultStopTimeout
	if plan.wasRunning {
		if err := cli.Stop(ctx, parentID, &stopTimeout); err != nil {
			return err
		}
	}
	if err := cli.Rename(ctx, parentID, recoveryName); err != nil {
		if plan.wasRunning {
			_ = cli.Start(context.Background(), parentID)
		}
		return err
	}

	newID, createErr := cli.Create(ctx, plan.config, plan.hostConfig, plan.networkConfig, plan.name)
	if createErr == nil && plan.wasRunning {
		createErr = cli.Start(ctx, newID)
	}
	if createErr == nil && plan.wasRunning {
		createErr = cli.WaitHealthy(ctx, newID, DefaultHealthTimeout, DefaultStabilityWindow)
	}
	if createErr == nil {
		if err := cli.Remove(ctx, parentID, true); err != nil {
			return fmt.Errorf("remove recovery container: %w", err)
		}
		emitSelfUpdateResult(dbPath, events.LevelInfo, "self_update.succeeded", plan.name, targetImage, "DoUpRo self-update succeeded")
		return nil
	}

	if newID != "" {
		_ = cli.Remove(context.Background(), newID, true)
	}
	if err := cli.Rename(context.Background(), parentID, plan.name); err != nil {
		return fmt.Errorf("replacement failed (%v) and recovery rename failed: %w", createErr, err)
	}
	if plan.wasRunning {
		if err := cli.Start(context.Background(), parentID); err != nil {
			return fmt.Errorf("replacement failed (%v) and recovery start failed: %w", createErr, err)
		}
	}
	emitSelfUpdateResult(dbPath, events.LevelError, "self_update.failed", plan.name, targetImage, fmt.Sprintf("replacement failed and original container was restored: %v", createErr))
	return fmt.Errorf("replacement failed; original container restored: %w", createErr)
}

func emitSelfUpdateResult(dbPath string, level events.Level, eventType, name, targetImage, message string) {
	logger := events.New("info")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if dbPath != "" {
		if st, err := store.Open(ctx, filepath.Clean(dbPath)); err == nil {
			logger.SetSink(st)
			defer st.Close() //nolint:errcheck // best-effort on helper exit
		}
	}
	logger.Emit(ctx, events.Event{Level: level, Type: eventType, Container: name, ToVersion: targetImage, Actor: events.ActorSystem, Message: message})
}
