// Package scheduler runs one-off ("update this container at time X") and
// recurring (cron or relative-delay policies applied to a stack) jobs,
// backed by the store's schedules table, and the periodic registry check
// that keeps update_available current. See docs/schedule.md.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/arnaudcharles/doupro/internal/docker"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/metrics"
	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/registry"
	"github.com/arnaudcharles/doupro/internal/store"
)

const unresolvedVersionRetry = 6 * time.Hour

var errVersionNotFound = errors.New("no concrete version tag matched the registry manifest digest")

type versionHints struct {
	current, available         string
	skipCurrent, skipAvailable bool
}

// Check discovers every visible container (running or stopped) on the
// Docker daemon into the store, and checks each one against its registry
// for a newer image. A failure anywhere here is logged, not fatal — the
// API and web UI must keep serving even if Docker or a registry is
// temporarily unreachable. Exported so both the daemon's startup pass and
// Scheduler's periodic tick call the exact same code path.
func Check(ctx context.Context, logger *events.Logger, cli *docker.Client, st *store.Store, notif *notifier.Notifier) {
	start := time.Now()
	defer func() {
		metrics.CheckDurationSeconds.Observe(time.Since(start).Seconds())
		metrics.LastCheckTimestamp.SetToCurrentTime()
	}()

	if err := cli.Ping(ctx); err != nil {
		logger.Emit(ctx, events.Event{
			Level:   events.LevelWarn,
			Type:    "docker.connect_failed",
			Actor:   events.ActorSystem,
			Message: fmt.Sprintf("docker daemon unreachable: %v", err),
		})
		return
	}

	containers, err := cli.List(ctx)
	if err != nil {
		logger.Emit(ctx, events.Event{
			Level:   events.LevelError,
			Type:    "docker.list_failed",
			Actor:   events.ActorSystem,
			Message: fmt.Sprintf("could not list containers: %v", err),
		})
		return
	}

	// Settings -> Exclusions (docs/settings.md) is an ad-hoc list on top of
	// the doupro.enable=false label docker.Client.List already resolved;
	// a lookup failure here just means no settings-based exclusions apply,
	// not a reason to abort the whole check.
	exclusions, _ := st.GetExclusions(ctx)
	excludedContainers := make(map[string]bool, len(exclusions.Containers))
	for _, name := range exclusions.Containers {
		excludedContainers[name] = true
	}
	excludedStacks := make(map[string]bool, len(exclusions.Stacks))
	for _, stack := range exclusions.Stacks {
		excludedStacks[stack] = true
	}

	liveIDs := make([]string, 0, len(containers))
	for i, c := range containers {
		if excludedContainers[c.Name] || excludedStacks[c.Stack] {
			containers[i].Excluded = true
			c.Excluded = true
		}

		record := store.ContainerRecord{
			ID:           c.ID,
			Name:         c.Name,
			Stack:        c.Stack,
			State:        c.State,
			Status:       c.Status,
			CurrentImage: c.Image,
			Excluded:     c.Excluded,
		}
		if err := st.UpsertSeen(ctx, record); err != nil {
			logger.Emit(ctx, events.Event{
				Level:     events.LevelError,
				Type:      "container.sync_failed",
				Container: c.Name,
				Actor:     events.ActorSystem,
				Message:   fmt.Sprintf("could not store container %s: %v", c.Name, err),
			})
		}
		// Still live per Docker even if the write above failed — a
		// transient store error is no reason to then have PruneContainers
		// treat it as gone and delete whatever row it already had.
		liveIDs = append(liveIDs, c.ID)
	}

	if err := st.PruneContainers(ctx, liveIDs); err != nil {
		logger.Emit(ctx, events.Event{
			Level:   events.LevelError,
			Type:    "container.sync_failed",
			Actor:   events.ActorSystem,
			Message: fmt.Sprintf("could not prune stale containers: %v", err),
		})
	}

	logger.Emit(ctx, events.Event{
		Level:   events.LevelInfo,
		Type:    "containers.discovered",
		Actor:   events.ActorSystem,
		Message: fmt.Sprintf("discovered %d container(s)", len(containers)),
		Metadata: map[string]any{
			"count": len(containers),
		},
	})

	checkForUpdates(ctx, logger, cli, st, notif, containers)
}

// checkForUpdates compares each container's locally recorded image digest
// against its registry's current digest for the same tag. This catches a
// moved tag (e.g. "latest" repointed to a new build) and selects compatible
// patch/minor/major candidates when the configured reference is strict semver.
func checkForUpdates(ctx context.Context, logger *events.Logger, cli *docker.Client, st *store.Store, notif *notifier.Notifier, containers []docker.Container) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	checked, available, failed := 0, 0, 0

	// Cache resolved registry tag listings per repository for the lifetime of
	// this one check run — several containers commonly share the same
	// image (e.g. a stack's replicas), and a repository's tags don't
	// change mid-run, so this avoids repeating the same Hub API calls
	// for every container that happens to use the same image.
	versionTagsCache := make(map[string]map[string]string)
	semverTagsCache := make(map[string]map[string]string)

	for _, c := range containers {
		dc, da, df := checkOneContainer(ctx, logger, cli, st, notif, httpClient, versionTagsCache, semverTagsCache, c)
		checked += dc
		available += da
		failed += df
	}

	logger.Emit(ctx, events.Event{
		Level:   events.LevelInfo,
		Type:    "containers.checked",
		Message: fmt.Sprintf("checked %d container(s) against their registry: %d update(s) available, %d check(s) failed", checked, available, failed),
		Metadata: map[string]any{
			"checked":   checked,
			"available": available,
			"failed":    failed,
		},
	})
}

// checkOneContainer runs the registry digest/version/semver-candidate check
// for a single container — the body of checkForUpdates' loop, pulled out so
// a single container can be force-checked on demand (see
// CheckOneContainerNow) without re-scanning the whole fleet. versionTagsCache
// and semverTagsCache are shared across a caller's containers when checking
// several at once (see checkForUpdates); pass fresh empty maps to check
// exactly one container in isolation.
func checkOneContainer(ctx context.Context, logger *events.Logger, cli *docker.Client, st *store.Store, notif *notifier.Notifier, httpClient *http.Client, versionTagsCache, semverTagsCache map[string]map[string]string, c docker.Container) (checked, available, failed int) {
	if c.Excluded || c.ImageID == "" {
		return 0, 0, 0
	}

	localDigests, err := cli.ImageDigests(ctx, c.ImageID)
	if err != nil || len(localDigests) == 0 {
		// No recorded digest (locally built image, or inspect failed) —
		// nothing to compare against, not an "update available" signal.
		return 0, 0, 0
	}
	// A rollback recreates from an immutable image ID, so Docker reports
	// Config.Image as either a raw sha256 digest or repo@sha256 afterward.
	// Keep using the stable compose-defined reference stored before the
	// rollback; immutable references have no moving tag to compare.
	imageRef := c.Image
	if registry.IsDigestReference(imageRef) {
		if rec, getErr := st.GetContainer(ctx, c.ID); getErr == nil {
			imageRef = registryImageRef(imageRef, rec.CurrentImage)
		}
	}
	if registry.IsDigestReference(imageRef) {
		// Older releases could persist the digest-qualified form. Recover
		// the last known tag from the durable update/rollback event trail.
		// If there is no history (for example a container deliberately
		// created from an image ID), skip it silently: querying a digest as
		// though it were repository:tag only creates a false 401/404 alert.
		if recovered, recoverErr := st.RecoverImageReference(ctx, c.Name); recoverErr == nil {
			imageRef = recovered
			if repairErr := st.RepairCurrentImage(ctx, c.ID, recovered); repairErr != nil {
				logger.Emit(ctx, events.Event{
					Level: events.LevelWarn, Type: "container.sync_failed", Container: c.Name,
					Actor:   events.ActorSystem,
					Message: fmt.Sprintf("recovered image reference for %s but could not store it: %v", c.Name, repairErr),
				})
			}
		}
	}
	if registry.IsDigestReference(imageRef) || strings.TrimSpace(imageRef) == "" {
		return 0, 0, 0
	}
	ref := registry.ParseRef(imageRef)
	localVersion, versionLabelErr := cli.ImageVersionLabel(ctx, c.ImageID, ref.Repository)
	if versionLabelErr != nil {
		logger.Emit(ctx, events.Event{Level: events.LevelWarn, Type: "version.label_read_failed", Container: c.Name,
			Actor: events.ActorSystem, Message: fmt.Sprintf("could not read image version metadata for %s: %v", c.Name, versionLabelErr)})
	}
	remoteDigest, err := registry.Digest(ctx, httpClient, ref)
	if err != nil {
		metrics.RegistryChecksTotal.WithLabelValues(ref.Registry, "failed").Inc()
		msg := fmt.Sprintf("registry check failed for %s (%s/%s:%s): %v", c.Name, ref.Registry, ref.Repository, ref.Tag, err)
		logger.Emit(ctx, events.Event{
			Level:     events.LevelWarn,
			Type:      "registry.check_failed",
			Container: c.Name,
			Actor:     events.ActorSystem,
			Message:   msg,
		})
		notif.Notify(ctx, "check.failed", c.Name, c.Stack, msg)
		return 0, 0, 1
	}
	metrics.RegistryChecksTotal.WithLabelValues(ref.Registry, "success").Inc()

	checked = 1
	// A newer image is only "available" if the registry's current
	// digest isn't already among the digests Docker has locally for
	// this image — a single image can carry more than one recorded
	// digest for the same tag (see ImageDigests), so membership, not
	// equality against one entry, is the correct comparison.
	hasUpdate := !slices.Contains(localDigests, remoteDigest)
	candidates := make(map[string]store.UpdateCandidate, 3)
	if hasUpdate {
		for _, scope := range []string{"patch", "minor", "major"} {
			candidates[scope] = store.UpdateCandidate{ContainerID: c.ID, Scope: scope, Image: imageRef, Version: ref.Tag}
		}
	}
	// A strict pinned semver tag normally never moves, so discover newer
	// tags and keep one durable candidate per allowed compatibility scope.
	if registry.IsStrictSemverTag(ref.Tag) {
		cacheKey := ref.Registry + "/" + ref.Repository
		tags, cached := semverTagsCache[cacheKey]
		if !cached {
			if fetched, listErr := registry.ListTags(ctx, httpClient, ref); listErr == nil {
				tags = fetched
			} else {
				logger.Emit(ctx, events.Event{Level: events.LevelWarn, Type: "registry.tags_failed", Container: c.Name,
					Actor: events.ActorSystem, Message: fmt.Sprintf("could not list semver tags for %s: %v", c.Name, listErr)})
			}
			semverTagsCache[cacheKey] = tags
		}
		for scope, tag := range registry.SelectSemverCandidates(tags, ref.Tag) {
			if slices.Contains(localDigests, tags[tag]) {
				continue
			}
			candidates[scope] = store.UpdateCandidate{ContainerID: c.ID, Scope: scope,
				Image: registry.ReplaceTag(ref, tag), Version: tag}
		}
		hasUpdate = len(candidates) > 0
	}
	if hasUpdate {
		available = 1
		// Only notify on the false->true transition, not on every
		// tick an already-known update stays available — otherwise a
		// container sitting on a pending update would re-notify every
		// check interval indefinitely. Best-effort: a lookup failure
		// just means "assume this is new" rather than blocking the
		// check.
		wasAvailable := false
		if prev, err := st.GetContainer(ctx, c.ID); err == nil {
			wasAvailable = prev.UpdateAvailable
		}
		if !wasAvailable {
			versions := map[string]string{}
			for scope, candidate := range candidates {
				versions[scope] = candidate.Version
			}
			logger.Emit(ctx, events.Event{Level: events.LevelInfo, Type: "update.candidate_detected", Container: c.Name,
				Stack: c.Stack, Actor: events.ActorSystem, Message: fmt.Sprintf("version candidate detected for %s", c.Name),
				Metadata: map[string]any{"versions": versions}})
			notif.Notify(ctx, "update.available", c.Name, c.Stack,
				fmt.Sprintf("update available for %s (%s)", c.Name, c.Image))
		}
	}
	candidateList := make([]store.UpdateCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidateList = append(candidateList, candidate)
	}
	if err := st.ReplaceUpdateCandidates(ctx, c.ID, candidateList, time.Now()); err != nil {
		logger.Emit(ctx, events.Event{Level: events.LevelError, Type: "container.sync_failed", Container: c.Name,
			Actor: events.ActorSystem, Message: fmt.Sprintf("could not store update candidates for %s: %v", c.Name, err)})
	}
	if err := st.SetUpdateAvailable(ctx, c.ID, hasUpdate); err != nil {
		logger.Emit(ctx, events.Event{
			Level:     events.LevelError,
			Type:      "container.sync_failed",
			Container: c.Name,
			Actor:     events.ActorSystem,
			Message:   fmt.Sprintf("could not record update status for %s: %v", c.Name, err),
		})
	}

	hints := loadVersionHints(ctx, st, ref, c.ID, localDigests[0], remoteDigest, hasUpdate, localVersion)
	currentVersion, availableVersion, resolveErr := resolveVersions(ctx, httpClient, versionTagsCache, ref, localDigests[0], remoteDigest, hasUpdate, hints)
	storeVersionResults(ctx, st, ref, localDigests[0], remoteDigest, hasUpdate, currentVersion, availableVersion, resolveErr)
	if resolveErr != nil {
		metrics.RegistryVersionResolutionsTotal.WithLabelValues(ref.Registry, "failed").Inc()
		logger.Emit(ctx, events.Event{Level: events.LevelWarn, Type: "registry.version_resolution_failed", Container: c.Name,
			Actor: events.ActorSystem, Message: fmt.Sprintf("could not resolve a registry version for %s: %v", c.Name, resolveErr)})
	} else if currentVersion != "" && !strings.HasPrefix(currentVersion, "sha256:") {
		metrics.RegistryVersionResolutionsTotal.WithLabelValues(ref.Registry, "success").Inc()
	}
	if major, ok := candidates["major"]; ok && registry.IsVersionTag(major.Version) {
		availableVersion = major.Version
	}
	if err := st.SetVersions(ctx, c.ID, currentVersion, availableVersion); err != nil {
		logger.Emit(ctx, events.Event{
			Level:     events.LevelError,
			Type:      "container.sync_failed",
			Container: c.Name,
			Actor:     events.ActorSystem,
			Message:   fmt.Sprintf("could not record resolved version for %s: %v", c.Name, err),
		})
	}
	return checked, available, failed
}

// CheckOneContainerNow forces an immediate registry check for a single
// container by name, outside the periodic Check() cycle — backs
// POST /api/v1/containers/{id}/check. Returns store.ErrNotFound if no such
// container is currently tracked.
func CheckOneContainerNow(ctx context.Context, logger *events.Logger, cli *docker.Client, st *store.Store, notif *notifier.Notifier, name string) (checked bool, updateAvailable bool, err error) {
	rec, err := st.GetContainerByName(ctx, name)
	if err != nil {
		return false, false, err
	}
	live, err := cli.List(ctx)
	if err != nil {
		return false, false, fmt.Errorf("list containers: %w", err)
	}
	var target docker.Container
	found := false
	for _, c := range live {
		if c.ID == rec.ID {
			target, found = c, true
			break
		}
	}
	if !found {
		return false, false, store.ErrNotFound
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	dc, _, _ := checkOneContainer(ctx, logger, cli, st, notif, httpClient,
		make(map[string]map[string]string), make(map[string]map[string]string), target)
	updated, err := st.GetContainerByName(ctx, name)
	if err != nil {
		return dc > 0, false, err
	}
	return dc > 0, updated.UpdateAvailable, nil
}

// ResolveVersionAfterAction resolves and stores the version for a container
// immediately after a successful update/rollback, instead of waiting for the
// next periodic Check() tick — see the Containers-page "Unresolved" UX
// complaint this addresses. Best-effort: any failure is logged and
// swallowed by the caller (api.handleUpdateContainer/handleRollbackContainer
// both ignore this returning an error), since it must never block or fail
// the update/rollback response itself.
func ResolveVersionAfterAction(ctx context.Context, logger *events.Logger, cli *docker.Client, st *store.Store, containerID, imageRef string) error {
	if registry.IsDigestReference(imageRef) || strings.TrimSpace(imageRef) == "" {
		return nil
	}
	ref := registry.ParseRef(imageRef)

	// Resolve from the immutable image ID of the container that is actually
	// running, not from imageRef. This distinction matters after Rollback:
	// imageRef is normally a floating compose-defined tag such as "latest",
	// and Docker still associates that tag with the newer image left in the
	// local cache. Looking up digests by tag would therefore label the rolled-
	// back container with the newer version even though it is running the old
	// image.
	currentVersion, availableVersion := "", ""
	inspect, inspectErr := cli.Inspect(ctx, containerID)
	if inspectErr == nil {
		localDigests, digestErr := cli.ImageDigests(ctx, inspect.Image)
		if digestErr == nil && len(localDigests) > 0 {
			httpClient := &http.Client{Timeout: 10 * time.Second}
			remoteDigest, remoteErr := registry.Digest(ctx, httpClient, ref)
			cache := make(map[string]map[string]string)
			localVersion, _ := cli.ImageVersionLabel(ctx, inspect.Image, ref.Repository)
			if remoteErr == nil {
				hasUpdate := !slices.Contains(localDigests, remoteDigest)
				hints := loadVersionHints(ctx, st, ref, containerID, localDigests[0], remoteDigest, hasUpdate, localVersion)
				currentVersion, availableVersion, _ = resolveVersions(ctx, httpClient, cache, ref, localDigests[0], remoteDigest, hasUpdate, hints)
			} else {
				// Registry availability must not prevent resolving the version that
				// is verifiably running. Do not invent an available version when the
				// remote digest itself could not be fetched.
				hints := loadVersionHints(ctx, st, ref, containerID, localDigests[0], localDigests[0], false, localVersion)
				currentVersion, _, _ = resolveVersions(ctx, httpClient, cache, ref, localDigests[0], localDigests[0], false, hints)
				availableVersion = ""
			}
		}
	}

	if err := st.SetVersions(ctx, containerID, currentVersion, availableVersion); err != nil {
		logger.Emit(ctx, events.Event{
			Level:     events.LevelWarn,
			Type:      "version.resolve_failed",
			Container: containerID,
			Actor:     events.ActorSystem,
			Message:   fmt.Sprintf("could not store resolved version for %s: %v", containerID, err),
		})
		return fmt.Errorf("store resolved version: %w", err)
	}

	return nil
}

func registryImageRef(liveImage, storedImage string) string {
	if registry.IsDigestReference(liveImage) && storedImage != "" && !registry.IsDigestReference(storedImage) {
		return storedImage
	}
	return liveImage
}

// resolveVersions turns currentDigest/remoteDigest into human-readable
// versions, for display only — never affects hasUpdate, which is already
// decided by the time this is called.
//
// The cheap, always-correct case first: if the container is already
// pinned to a tag that looks like a real version (e.g. "grafana:11.5.3"),
// that tag *is* the running version — no registry call needed, and no
// risk of missing it.
//
// For a floating tag, the immutable local image's standard OCI version label
// is preferred. If the publisher omitted it, resolve selected manifest
// digests back through version-shaped tags: Docker Hub's digest-bearing API
// or the standard OCI tags/list + manifest endpoints for every other registry.
//
// If the publisher exposes neither a concrete label nor a version-shaped tag,
// the digest remains the honest fallback and a structured resolution failure
// is emitted; a semantic version is never fabricated.
func resolveVersions(ctx context.Context, httpClient *http.Client, cache map[string]map[string]string, ref registry.Ref, currentDigest, remoteDigest string, hasUpdate bool, hints versionHints) (currentVersion, availableVersion string, err error) {
	if registry.IsVersionTag(ref.Tag) {
		// Same pinned tag either way — if hasUpdate, it was just repointed
		// to a different build, which is still the most accurate label we
		// have for the target.
		return ref.Tag, ref.Tag, nil
	}

	currentVersion = currentDigest
	if hints.current != "" {
		currentVersion = hints.current
	} else if !hints.skipCurrent {
		if label, labelErr := registry.VersionLabelForDigest(ctx, httpClient, ref, currentDigest); labelErr == nil && label != "" {
			currentVersion, hints.current = label, label
		}
	}
	if hasUpdate {
		availableVersion = remoteDigest
		if hints.available != "" {
			availableVersion = hints.available
		} else if !hints.skipAvailable {
			if label, labelErr := registry.VersionLabelForDigest(ctx, httpClient, ref, remoteDigest); labelErr == nil && label != "" {
				availableVersion, hints.available = label, label
			}
		}
	} else {
		availableVersion = currentVersion
	}

	// Multiple containers can share a repository but not necessarily the
	// same digest (e.g. two unrelated containers both on a floating tag
	// of the same image, recreated at different times) — trust the
	// cache only if it already resolves everything this container
	// needs; otherwise extend it with a fetch scoped to what's missing,
	// rather than either always re-fetching (defeats the point of
	// caching) or trusting a cache built for a different digest (would
	// silently under-resolve).
	cacheKey := ref.Registry + "/" + ref.Repository
	tags := cache[cacheKey]
	missing := make(map[string]bool, 2)
	if hints.current == "" && !hints.skipCurrent {
		if _, ok := registry.ResolveVersion(tags, currentDigest); !ok {
			missing[currentDigest] = true
		}
	}
	if hasUpdate && !hints.skipAvailable {
		if strings.HasPrefix(availableVersion, "sha256:") {
			if _, ok := registry.ResolveVersion(tags, remoteDigest); !ok {
				missing[remoteDigest] = true
			}
		}
	}

	if len(missing) > 0 {
		fetched, fetchErr := registry.ResolveVersionTags(ctx, httpClient, ref, missing)
		if fetchErr == nil {
			if tags == nil {
				tags = make(map[string]string, len(fetched))
			}
			for name, digest := range fetched {
				tags[name] = digest
			}
			cache[cacheKey] = tags
		} else {
			err = fetchErr
		}
	}
	if len(tags) == 0 {
		if err == nil && ((!hints.skipCurrent && strings.HasPrefix(currentVersion, "sha256:")) ||
			(hasUpdate && !hints.skipAvailable && strings.HasPrefix(availableVersion, "sha256:"))) {
			err = errVersionNotFound
		}
		return currentVersion, availableVersion, err
	}

	if v, ok := registry.ResolveVersion(tags, currentDigest); ok {
		currentVersion = v
	}
	if hasUpdate {
		if v, ok := registry.ResolveVersion(tags, remoteDigest); ok {
			availableVersion = v
		}
	} else {
		availableVersion = currentVersion
	}
	if err == nil && ((!hints.skipCurrent && strings.HasPrefix(currentVersion, "sha256:")) ||
		(hasUpdate && !hints.skipAvailable && strings.HasPrefix(availableVersion, "sha256:"))) {
		err = errVersionNotFound
	}
	return currentVersion, availableVersion, err
}

func loadVersionHints(ctx context.Context, st *store.Store, ref registry.Ref, containerID, currentDigest, remoteDigest string, hasUpdate bool, localVersion string) versionHints {
	hints := versionHints{current: localVersion}
	load := func(digest string) (string, bool) {
		rec, ok, err := st.GetImageVersion(ctx, ref.Registry, ref.Repository, digest)
		if err != nil || !ok {
			metrics.RegistryVersionCacheTotal.WithLabelValues("miss").Inc()
			return "", false
		}
		if rec.Status == "resolved" && registry.IsVersionTag(rec.Version) {
			metrics.RegistryVersionCacheTotal.WithLabelValues("resolved").Inc()
			return rec.Version, false
		}
		if rec.Status == "unresolved" && time.Since(rec.CheckedAt) < unresolvedVersionRetry {
			metrics.RegistryVersionCacheTotal.WithLabelValues("unresolved").Inc()
			return "", true
		}
		metrics.RegistryVersionCacheTotal.WithLabelValues("expired").Inc()
		return "", false
	}
	if hints.current == "" {
		hints.current, hints.skipCurrent = load(currentDigest)
		if hints.current == "" && !hints.skipCurrent {
			if existing, err := st.GetContainer(ctx, containerID); err == nil && registry.IsVersionTag(existing.CurrentVersion) {
				hints.current = existing.CurrentVersion
			}
		}
	}
	if hasUpdate {
		hints.available, hints.skipAvailable = load(remoteDigest)
	}
	return hints
}

func storeVersionResults(ctx context.Context, st *store.Store, ref registry.Ref, currentDigest, remoteDigest string, hasUpdate bool, currentVersion, availableVersion string, resolveErr error) {
	storeOne := func(digest, version string) {
		rec := store.ImageVersionRecord{Registry: ref.Registry, Repository: ref.Repository, Digest: digest, CheckedAt: time.Now().UTC()}
		if registry.IsVersionTag(version) {
			rec.Version, rec.Status = version, "resolved"
		} else if errors.Is(resolveErr, errVersionNotFound) {
			rec.Status = "unresolved"
		} else {
			return
		}
		_ = st.PutImageVersion(ctx, rec)
	}
	storeOne(currentDigest, currentVersion)
	if hasUpdate {
		storeOne(remoteDigest, availableVersion)
	}
}
