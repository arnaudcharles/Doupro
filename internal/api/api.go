// Package api implements the REST API under /api/v1: handlers, request/
// response DTOs, session/API-key auth middleware, and the OpenAPI document
// served at /api/v1/openapi.json. Every feature exposed by the CLI or web
// UI is backed by an endpoint here first. See docs/api.md.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/arnaudcharles/doupro/internal/auth"
	"github.com/arnaudcharles/doupro/internal/docker"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/notifier"
	oidcauth "github.com/arnaudcharles/doupro/internal/oidc"
	"github.com/arnaudcharles/doupro/internal/registry"
	"github.com/arnaudcharles/doupro/internal/scheduler"
	"github.com/arnaudcharles/doupro/internal/store"
	"github.com/arnaudcharles/doupro/internal/updater"
)

//go:embed openapi.json
var openAPISpec []byte

// versionResolver is a function passed to RegisterRoutes to resolve versions
// after a successful update/rollback. It's implemented by the scheduler
// package to avoid a circular dependency.
type versionResolver func(ctx context.Context, containerID string, imageRef string) error

// operationTimeout bounds a user-triggered Docker mutation even when the
// client that submitted it disconnects. Pulling a large image and waiting for
// the post-recreate health check can legitimately outlive a browser request.
const operationTimeout = 15 * time.Minute

// operationContext detaches a durable Docker operation from the HTTP request
// lifecycle while retaining request-scoped values (for example a request ID).
// A browser navigation, reverse-proxy timeout or closed CLI connection must
// never cancel an update halfway through Docker's stop/pull/recreate sequence.
func operationContext(requestCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(requestCtx), operationTimeout)
}

// RegisterRoutes mounts the API onto mux. Routes are added here as each
// endpoint in docs/api.md gets implemented; internal/web mounts the UI on
// the same mux, sharing one HTTP server per docs/architecture.md. Call
// Bootstrap before this in a running daemon (see cmd/doupro).
// trustProxyHeaders is DOUPRO_TRUST_PROXY_HEADERS (see
// auth.IsSecureRequest) — only set true when a reverse proxy terminating
// TLS actually sits in front of DoUpRo and strips any client-supplied
// X-Forwarded-Proto before setting its own.
func RegisterRoutes(mux *http.ServeMux, st *store.Store, upd *updater.Updater, notif *notifier.Notifier, logger *events.Logger, trustProxyHeaders bool, oidcProvider *oidcauth.Provider, resolveVer versionResolver, cli *docker.Client) {
	loginLimiter := newRateLimiter(5, 5*time.Minute)

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /api/v1/openapi.json", handleOpenAPISpec)
	mux.HandleFunc("POST /api/v1/auth/login", loginLimiter.wrap(handleLogin(st, logger, trustProxyHeaders)))
	mux.HandleFunc("POST /api/v1/auth/logout", handleLogout(st, logger))
	if oidcProvider != nil {
		mux.HandleFunc("GET /api/v1/auth/oidc/login", handleOIDCLogin(oidcProvider, trustProxyHeaders))
		mux.HandleFunc("GET /api/v1/auth/oidc/callback", handleOIDCCallback(oidcProvider, st, logger, trustProxyHeaders))
	}

	protected := auth.RequireAuth(st)
	withPermission := func(permission store.Permission, handler http.Handler) http.Handler {
		return protected(auth.RequirePermission(permission)(handler))
	}
	passwordLimiter := newRateLimiter(5, 5*time.Minute)
	mux.Handle("POST /api/v1/auth/password", protected(passwordLimiter.wrap(handleChangePassword(st, logger))))
	mux.Handle("GET /api/v1/containers", withPermission(store.PermissionReadContainers, http.HandlerFunc(handleListContainers(st, upd))))
	mux.Handle("GET /api/v1/logs", withPermission(store.PermissionViewLogs, http.HandlerFunc(handleListLogs(st))))
	// {id} is matched against the container *name*, not Docker's container
	// ID, which changes on every recreate — the name is what stays stable
	// and what operators actually refer to (docs/cli.md examples all use
	// the name, e.g. `doupro update adguard-home`).
	mux.Handle("GET /api/v1/containers/{id}", withPermission(store.PermissionReadContainers, http.HandlerFunc(handleGetContainer(st, upd))))
	mux.Handle("POST /api/v1/containers/{id}/check", withPermission(store.PermissionWriteContainers, http.HandlerFunc(handleCheckContainer(st, cli, logger, notif))))
	// GET previews exactly what POST on the same path would do — a
	// read-only dry-run, gated on read (not write) access accordingly.
	mux.Handle("GET /api/v1/containers/{id}/update", withPermission(store.PermissionReadContainers, http.HandlerFunc(handlePreviewUpdate(st))))
	mux.Handle("POST /api/v1/containers/{id}/update", withPermission(store.PermissionWriteContainers, http.HandlerFunc(handleUpdateContainer(st, upd, resolveVer))))
	mux.Handle("POST /api/v1/containers/{id}/rollback", withPermission(store.PermissionWriteContainers, http.HandlerFunc(handleRollbackContainer(st, upd, resolveVer))))
	// A distinct path shape from {id}/update (one segment shorter), so
	// there's no route-matching ambiguity with a container literally
	// named "update-all".
	mux.Handle("POST /api/v1/containers/update-all", withPermission(store.PermissionWriteContainers, http.HandlerFunc(handleUpdateAllContainers(st, upd, resolveVer, logger))))

	mux.Handle("GET /api/v1/settings/general", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleGetGeneralSettings(st))))
	mux.Handle("PATCH /api/v1/settings/general", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleSetGeneralSettings(st, logger))))
	mux.Handle("GET /api/v1/settings/exclusions", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleGetExclusions(st))))
	mux.Handle("PATCH /api/v1/settings/exclusions", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleSetExclusions(st, logger))))
	mux.Handle("GET /api/v1/settings/registries", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleGetRegistries(st))))
	mux.Handle("PATCH /api/v1/settings/registries", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleSetRegistries(st, logger))))
	mux.Handle("POST /api/v1/settings/registries/test", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleTestRegistry(st, logger))))
	mux.Handle("GET /api/v1/settings/security/api-keys", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleListAPIKeys(st))))
	mux.Handle("POST /api/v1/settings/security/api-keys", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleCreateAPIKey(st, logger))))
	mux.Handle("DELETE /api/v1/settings/security/api-keys/{id}", withPermission(store.PermissionManageSettings, http.HandlerFunc(handleRevokeAPIKey(st, logger))))
	mux.Handle("GET /api/v1/users", withPermission(store.PermissionManageUsers, http.HandlerFunc(handleListUsers(st))))
	mux.Handle("POST /api/v1/users", withPermission(store.PermissionManageUsers, http.HandlerFunc(handleCreateUser(st, logger))))
	mux.Handle("PATCH /api/v1/users/{id}", withPermission(store.PermissionManageUsers, http.HandlerFunc(handleUpdateUserAccess(st, logger))))
	mux.Handle("DELETE /api/v1/users/{id}", withPermission(store.PermissionManageUsers, http.HandlerFunc(handleDeleteUser(st, logger))))

	mux.Handle("GET /api/v1/stats", withPermission(store.PermissionViewStats, http.HandlerFunc(handleStatsSummary(st))))

	mux.Handle("GET /api/v1/notifications", withPermission(store.PermissionManageNotifications, http.HandlerFunc(handleListNotifications(st))))
	mux.Handle("GET /api/v1/notifications/queue", withPermission(store.PermissionManageNotifications, http.HandlerFunc(handleListNotificationQueue(st))))
	mux.Handle("POST /api/v1/notifications/queue/{id}/retry", withPermission(store.PermissionManageNotifications, http.HandlerFunc(handleRetryNotification(st, logger))))
	mux.Handle("GET /api/v1/settings/notifications", withPermission(store.PermissionManageNotifications, http.HandlerFunc(handleGetNotificationChannels(notif))))
	mux.Handle("PATCH /api/v1/settings/notifications", withPermission(store.PermissionManageNotifications, http.HandlerFunc(handleSetNotificationChannels(notif, logger))))
	mux.Handle("POST /api/v1/settings/notifications/test", withPermission(store.PermissionManageNotifications, http.HandlerFunc(handleTestNotificationChannel(notif))))

	mux.Handle("GET /api/v1/schedules", withPermission(store.PermissionManageSchedules, http.HandlerFunc(handleListSchedules(st))))
	mux.Handle("POST /api/v1/schedules", withPermission(store.PermissionManageSchedules, http.HandlerFunc(handleCreateSchedule(st, notif))))
	mux.Handle("PATCH /api/v1/schedules/{id}", withPermission(store.PermissionManageSchedules, http.HandlerFunc(handleUpdateSchedule(st, notif))))
	mux.Handle("DELETE /api/v1/schedules/{id}", withPermission(store.PermissionManageSchedules, http.HandlerFunc(handleDeleteSchedule(st, notif))))
}

type healthResponse struct {
	Status string `json:"status"`
}

// handleHealth backs the Docker HEALTHCHECK and orchestrator probes.
// Intentionally outside /api/v1 and outside auth (see docs/api.md) since
// it must be reachable before any session or API key exists.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// handleOpenAPISpec serves the hand-written OpenAPI 3.0 document
// (openapi.json, embedded at build time) that backs the Swagger UI at
// /swagger and any external tooling that wants a machine-readable
// description of this API.
func handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPISpec)
}

// containerResponse mirrors the container detail shape documented in
// docs/api.md. current_tag/version_source let clients distinguish an exact
// publisher version from the source tag or digest fallback.
type containerResponse struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Stack            string               `json:"stack"`
	State            string               `json:"state"`
	Status           string               `json:"status"`
	CurrentImage     string               `json:"current_image"`
	PreviousImage    string               `json:"previous_image,omitempty"`
	CurrentVersion   string               `json:"current_version,omitempty"`
	CurrentTag       string               `json:"current_tag,omitempty"`
	VersionSource    string               `json:"version_source"`
	AvailableVersion string               `json:"available_version,omitempty"`
	UpdateKind       string               `json:"update_kind,omitempty"`
	UpdateAvailable  bool                 `json:"update_available"`
	Excluded         bool                 `json:"excluded"`
	LastCheckedAt    *string              `json:"last_checked_at,omitempty"`
	Operation        *updater.Operation   `json:"operation,omitempty"`
	RollbackWatch    *store.RollbackWatch `json:"rollback_watch,omitempty"`
}

func handleListContainers(st *store.Store, upd *updater.Updater) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		records, err := st.ListContainers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to list containers")
			return
		}

		resp := make([]containerResponse, 0, len(records))
		for _, c := range records {
			resp = append(resp, toContainerResponse(r.Context(), st, upd, c))
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// toContainerResponse builds one container's API detail shape — shared by
// the list and single-container detail handlers so they can never drift.
func toContainerResponse(ctx context.Context, st *store.Store, upd *updater.Updater, c store.ContainerRecord) containerResponse {
	currentTag, versionSource := containerVersionIdentity(c.CurrentImage, c.CurrentVersion)
	cr := containerResponse{
		ID:               c.ID,
		Name:             c.Name,
		Stack:            c.Stack,
		State:            c.State,
		Status:           c.Status,
		CurrentImage:     c.CurrentImage,
		PreviousImage:    c.PreviousImage,
		CurrentVersion:   c.CurrentVersion,
		CurrentTag:       currentTag,
		VersionSource:    versionSource,
		AvailableVersion: c.AvailableVersion,
		UpdateKind:       registry.ClassifyUpdate(c.CurrentVersion, c.AvailableVersion, c.UpdateAvailable),
		UpdateAvailable:  c.UpdateAvailable,
		Excluded:         c.Excluded,
	}
	if c.LastCheckedAt != nil {
		s := c.LastCheckedAt.Format(time.RFC3339)
		cr.LastCheckedAt = &s
	}
	if op, ok := upd.Operation(c.Name); ok {
		cr.Operation = &op
	}
	if watch, getErr := st.GetRollbackWatch(ctx, c.Name); getErr == nil && watch.ContainerID == c.ID && (watch.Status == "active" || watch.Status == "triggered") {
		cr.RollbackWatch = &watch
	}
	return cr
}

// handleGetContainer backs GET /api/v1/containers/{id}: detail for one
// tracked container, the same shape as one entry from the list endpoint.
func handleGetContainer(st *store.Store, upd *updater.Updater) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, err := st.GetContainerByName(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "container not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to look up container")
			return
		}
		writeJSON(w, http.StatusOK, toContainerResponse(r.Context(), st, upd, rec))
	}
}

// checkResponse is the outcome of a forced single-container registry check.
type checkResponse struct {
	Checked         bool `json:"checked"`
	UpdateAvailable bool `json:"update_available"`
}

// handleCheckContainer backs POST /api/v1/containers/{id}/check: forces an
// immediate registry check for one container outside the periodic cycle,
// reusing the exact same per-container logic the fleet-wide check uses
// (scheduler.CheckOneContainerNow) so the two can never diverge in
// behavior. checked is false (not an error) when the container has no
// recorded local digest to compare against — a locally built image, for
// instance — the same "nothing to compare" case the periodic check
// silently skips.
func handleCheckContainer(st *store.Store, cli *docker.Client, logger *events.Logger, notif *notifier.Notifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checked, updateAvailable, err := scheduler.CheckOneContainerNow(r.Context(), logger, cli, st, notif, r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "container not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, "check_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, checkResponse{Checked: checked, UpdateAvailable: updateAvailable})
	}
}

func containerVersionIdentity(currentImage, currentVersion string) (tag, source string) {
	if currentVersion != "" && !strings.HasPrefix(currentVersion, "sha256:") {
		source = "version"
	}
	if currentImage != "" && !registry.IsDigestReference(currentImage) {
		tag = registry.ParseRef(currentImage).Tag
	}
	if source != "" {
		return tag, source
	}
	if tag != "" {
		return tag, "tag"
	}
	if strings.HasPrefix(currentVersion, "sha256:") || registry.IsDigestReference(currentImage) {
		return "", "digest"
	}
	return "", "none"
}

// parseSemverScope validates the ?semver= query param shared by the
// update, update preview, and update-all-adjacent endpoints, defaulting to
// "major" (the most permissive scope — any newer version, not just a
// patch/minor-compatible one) when omitted.
func parseSemverScope(r *http.Request) (string, error) {
	scope := r.URL.Query().Get("semver")
	if scope == "" {
		scope = "major"
	}
	if scope != "patch" && scope != "minor" && scope != "major" {
		return "", fmt.Errorf("semver must be patch, minor, or major")
	}
	return scope, nil
}

// resolveUpdateTarget returns the image reference and human-readable
// version Update would actually pull for rec at the given semver scope —
// a durable candidate recorded by the periodic/on-demand registry check
// (internal/scheduler.checkOneContainer) if one exists for that scope,
// else rec's own current image/available version (the "just re-pull
// whatever the tag currently resolves to" fallback for a container with
// no specific candidate on record). Shared by the real update and its
// preview so the two can never show/do different things.
//
// A candidate's Version is only trusted as a *version* when it's actually
// version-shaped (registry.IsVersionTag) — checkOneContainer stamps every
// candidate's Version with the raw configured tag as a placeholder even
// for a floating tag like "latest", which is not a version and would
// otherwise show up as a misleading "target version" here. This mirrors
// exactly the same check checkOneContainer itself applies before trusting
// a candidate's Version as the container's AvailableVersion.
func resolveUpdateTarget(ctx context.Context, st *store.Store, rec store.ContainerRecord, scope string) (targetImage, targetVersion string) {
	targetImage, targetVersion = rec.CurrentImage, rec.AvailableVersion
	if candidate, err := st.GetUpdateCandidate(ctx, rec.ID, scope); err == nil {
		targetImage = candidate.Image
		if registry.IsVersionTag(candidate.Version) {
			targetVersion = candidate.Version
		}
	}
	return targetImage, targetVersion
}

// updatePreviewResponse is the read-only "what would Update do" shape
// returned by GET .../update — see handlePreviewUpdate.
type updatePreviewResponse struct {
	CurrentImage    string `json:"current_image"`
	CurrentVersion  string `json:"current_version,omitempty"`
	TargetImage     string `json:"target_image"`
	TargetVersion   string `json:"target_version,omitempty"`
	UpdateKind      string `json:"update_kind,omitempty"`
	SemverScope     string `json:"semver_scope"`
	UpdateAvailable bool   `json:"update_available"`
}

// handlePreviewUpdate backs GET /api/v1/containers/{id}/update — the
// dry-run counterpart to POST on the same path: resolves exactly what an
// Update at the given ?semver= scope would target, without pulling or
// touching Docker at all, so an operator (or a script) can see the
// concrete before/after before committing to it.
func handlePreviewUpdate(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, err := st.GetContainerByName(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "container not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to look up container")
			return
		}
		scope, err := parseSemverScope(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		targetImage, targetVersion := resolveUpdateTarget(r.Context(), st, rec, scope)
		writeJSON(w, http.StatusOK, updatePreviewResponse{
			CurrentImage:    rec.CurrentImage,
			CurrentVersion:  rec.CurrentVersion,
			TargetImage:     targetImage,
			TargetVersion:   targetVersion,
			UpdateKind:      registry.ClassifyUpdate(rec.CurrentVersion, targetVersion, rec.UpdateAvailable),
			SemverScope:     scope,
			UpdateAvailable: rec.UpdateAvailable,
		})
	}
}

// handleUpdateContainer backs POST /api/v1/containers/{id}/update: pulls
// the container's current image reference again (picking up whatever
// digest it now resolves to — see docs/containers.md on digest-based
// detection) and recreates the container, keeping the prior image on
// record for rollback. On success, resolveVersions is called to populate
// the version strings for immediate display.
func handleUpdateContainer(st *store.Store, upd *updater.Updater, resolveVer versionResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, err := st.GetContainerByName(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "container not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to look up container")
			return
		}

		actorID := auth.ActorFromContext(r.Context())
		scope, err := parseSemverScope(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		target, _ := resolveUpdateTarget(r.Context(), st, rec, scope)
		if upd.IsSelf(r.Context(), rec.ID) {
			err = upd.StartSelfUpdate(r.Context(), rec.ID, target, actorID)
			respondSelfUpdate(w, r, err)
			return
		}
		opCtx, cancel := operationContext(r.Context())
		defer cancel()
		err = upd.Update(opCtx, rec.ID, target, events.ActorUser, actorID)
		if err == nil {
			_ = st.ClearUpdateCandidates(opCtx, rec.ID)
		}
		if err == nil && resolveVer != nil {
			// Best-effort version resolution after successful update.
			// Fetch the updated record and resolve its new image reference.
			if updated, getErr := st.GetContainerByName(opCtx, rec.Name); getErr == nil {
				_ = resolveVer(opCtx, updated.ID, updated.CurrentImage)
			}
		}
		respondAction(w, r, err, "update_failed")
	}
}

func respondSelfUpdate(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		var unsafe *updater.SelfUpdateSafetyError
		code := "self_update_failed"
		if errors.As(err, &unsafe) {
			code = "self_update_unsafe"
		}
		if isJSON(r) {
			writeError(w, http.StatusConflict, code, err.Error())
			return
		}
		http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if isJSON(r) {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>DoUpRo restarting</title></head><body style="font-family:system-ui;background:#111827;color:#e5e7eb;padding:3rem"><h1>DoUpRo is updating safely…</h1><p>The page will reload when the new instance is healthy. If it fails, the previous instance will be restored automatically.</p><script>const poll=()=>fetch('/health',{cache:'no-store'}).then(r=>{if(r.ok)location.href='/';else setTimeout(poll,1500)}).catch(()=>setTimeout(poll,1500));setTimeout(poll,2500)</script></body></html>`))
}

// handleRollbackContainer backs POST /api/v1/containers/{id}/rollback:
// recreates the container from its recorded previous image. Available
// any time a previous version is on record — see docs/containers.md.
// On success, resolveVersions is called to populate the version strings
// for immediate display.
func handleRollbackContainer(st *store.Store, upd *updater.Updater, resolveVer versionResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, err := st.GetContainerByName(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "container not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to look up container")
			return
		}

		actorID := auth.ActorFromContext(r.Context())
		opCtx, cancel := operationContext(r.Context())
		defer cancel()
		err = upd.Rollback(opCtx, rec.ID, events.ActorUser, actorID)
		if err == nil && resolveVer != nil {
			// Best-effort version resolution after successful rollback.
			// Fetch the rolled-back record and resolve its current image reference.
			if rolled, getErr := st.GetContainerByName(opCtx, rec.Name); getErr == nil {
				_ = resolveVer(opCtx, rolled.ID, rolled.CurrentImage)
			}
		}
		respondAction(w, r, err, "rollback_failed")
	}
}

// handleUpdateAllContainers backs POST /api/v1/containers/update-all —
// triggers Update for every currently-updatable container, optionally
// scoped to one stack via ?stack=. Excluded containers and DoUpRo's own
// container are always skipped, matching "except exceptions" from the
// Containers page. Processing happens in the background (like
// self-update's 202 pattern) since a batch of many containers can take
// far longer than an HTTP client should be held open for; the response
// only reports how many were queued, and per-container progress is
// already visible via the existing Operation field on GET /containers.
func handleUpdateAllContainers(st *store.Store, upd *updater.Updater, resolveVer versionResolver, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stack := r.URL.Query().Get("stack")
		records, err := st.ListContainers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to list containers")
			return
		}
		targets := selectBulkUpdateTargets(records, stack)
		targets = excludeSelf(r.Context(), upd, targets)
		actorID := auth.ActorFromContext(r.Context())

		if len(targets) > 0 {
			scope := "every stack"
			if stack != "" {
				scope = "stack " + stack
			}
			logger.Emit(r.Context(), events.Event{
				Level: events.LevelInfo, Type: "containers.bulk_update_started", Actor: events.ActorUser, ActorID: actorID,
				Message:  fmt.Sprintf("bulk update started for %d container(s) in %s", len(targets), scope),
				Metadata: map[string]any{"count": len(targets), "stack": stack},
			})
			opCtx, cancel := operationContext(r.Context())
			go func() {
				defer cancel()
				runBulkUpdate(opCtx, st, upd, resolveVer, logger, targets, actorID)
			}()
		}

		writeJSON(w, http.StatusAccepted, map[string]int{"queued": len(targets)})
	}
}

// selectBulkUpdateTargets is the pure filtering half of
// handleUpdateAllContainers — kept separate from the I/O (self-detection,
// actually running the updates) so the "except exceptions" selection logic
// has direct test coverage.
func selectBulkUpdateTargets(records []store.ContainerRecord, stack string) []store.ContainerRecord {
	targets := make([]store.ContainerRecord, 0, len(records))
	for _, c := range records {
		if !c.UpdateAvailable || c.Excluded {
			continue
		}
		if stack != "" && c.Stack != stack {
			continue
		}
		targets = append(targets, c)
	}
	return targets
}

// excludeSelf drops DoUpRo's own container from a bulk batch — self-update
// has its own safety gates (maintenance window, schedule horizon) that a
// plain bulk Update call doesn't go through, so it's excluded rather than
// silently mis-triggered.
func excludeSelf(ctx context.Context, upd *updater.Updater, targets []store.ContainerRecord) []store.ContainerRecord {
	out := targets[:0]
	for _, c := range targets {
		if !upd.IsSelf(ctx, c.ID) {
			out = append(out, c)
		}
	}
	return out
}

// runBulkUpdate applies Update to each target in turn, continuing past a
// per-container failure or lock contention instead of aborting the whole
// batch — one stuck/rate-limited container must not block the rest of the
// fleet. Each attempt already gets its own update.started/succeeded/failed
// event and notification from Updater.Update; this only adds one summary
// event once the batch finishes.
func runBulkUpdate(ctx context.Context, st *store.Store, upd *updater.Updater, resolveVer versionResolver, logger *events.Logger, targets []store.ContainerRecord, actorID string) {
	succeeded, failed := 0, 0
	for _, rec := range targets {
		err := upd.Update(ctx, rec.ID, rec.CurrentImage, events.ActorUser, actorID)
		if err != nil {
			failed++
			continue
		}
		succeeded++
		_ = st.ClearUpdateCandidates(ctx, rec.ID)
		if resolveVer != nil {
			if updated, getErr := st.GetContainerByName(ctx, rec.Name); getErr == nil {
				_ = resolveVer(ctx, updated.ID, updated.CurrentImage)
			}
		}
	}
	logger.Emit(ctx, events.Event{
		Level: events.LevelInfo, Type: "containers.bulk_update_completed", Actor: events.ActorUser, ActorID: actorID,
		Message:  fmt.Sprintf("bulk update finished: %d succeeded, %d failed", succeeded, failed),
		Metadata: map[string]any{"succeeded": succeeded, "failed": failed},
	})
}

// respondAction writes the outcome of an update/rollback: JSON for
// API/CLI callers, a redirect back to the Containers page for a plain
// HTML form post from the web UI (see containers.html) — there's no
// htmx/fragment swap yet, so a full page reload is what shows the result.
func respondAction(w http.ResponseWriter, r *http.Request, err error, errCode string) {
	if isJSON(r) {
		if err != nil {
			var inProgress *updater.OperationInProgressError
			if errors.As(err, &inProgress) {
				writeError(w, http.StatusConflict, "operation_in_progress", err.Error())
				return
			}
			writeError(w, http.StatusConflict, errCode, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if err != nil {
		http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// logResponse mirrors the event schema in docs/logs.md.
type logResponse struct {
	ID          int64          `json:"id"`
	Timestamp   string         `json:"timestamp"`
	Level       string         `json:"level"`
	Event       string         `json:"event"`
	Container   string         `json:"container,omitempty"`
	Stack       string         `json:"stack,omitempty"`
	FromVersion string         `json:"from_version,omitempty"`
	ToVersion   string         `json:"to_version,omitempty"`
	Actor       string         `json:"actor,omitempty"`
	ActorID     string         `json:"actor_id,omitempty"`
	RequestID   string         `json:"request_id,omitempty"`
	Message     string         `json:"message"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// handleListLogs backs GET /api/v1/logs, supporting the same filters as
// the Logs page (?event=&container=&level=&limit=), see docs/logs.md and
// docs/api.md. ?download=1 (what the Logs page's Export button links to)
// changes nothing about the query — same filters, same JSON array — only
// adds a Content-Disposition header so the browser saves it as a file
// instead of navigating to the raw JSON.
func handleListLogs(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter := store.EventFilter{
			EventType: r.URL.Query().Get("event"),
			Container: r.URL.Query().Get("container"),
			Level:     r.URL.Query().Get("level"),
		}
		if limit, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
			filter.Limit = limit
		}

		records, err := st.ListEvents(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to list logs")
			return
		}

		resp := make([]logResponse, 0, len(records))
		for _, e := range records {
			resp = append(resp, logResponse{
				ID:          e.ID,
				Timestamp:   e.Timestamp.Format(time.RFC3339),
				Level:       e.Level,
				Event:       e.Type,
				Container:   e.Container,
				Stack:       e.Stack,
				FromVersion: e.FromVersion,
				ToVersion:   e.ToVersion,
				Actor:       e.Actor,
				ActorID:     e.ActorID,
				RequestID:   e.RequestID,
				Message:     e.Message,
				Metadata:    e.Metadata,
			})
		}

		if r.URL.Query().Get("download") != "" {
			filename := fmt.Sprintf("doupro-logs-%s.json", time.Now().UTC().Format("20060102-150405"))
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError matches the {"error": {"code", "message"}} shape documented
// in docs/api.md.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
