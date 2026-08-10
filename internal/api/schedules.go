package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/registry"
	"github.com/arnaudcharles/doupro/internal/scheduler"
	"github.com/arnaudcharles/doupro/internal/store"
)

// scheduleHTTPClient is used only to resolve a digest at schedule-creation
// time (pinImage) — short timeout since it's on the request path, not a
// background job.
var scheduleHTTPClient = &http.Client{Timeout: 8 * time.Second}

// pinImage resolves currentImage's tag to the registry's current digest
// and returns a digest-qualified reference (e.g. "nginx@sha256:abcd...")
// — a genuine, immutable pin, unlike storing the plain tag. Without this,
// "once" schedules on a floating tag (e.g. "latest") weren't really
// pinned at all: recreate() pulls whatever targetImage names, and a bare
// tag still resolves to whatever the tag points to *at execution time*,
// silently contradicting docs/containers.md's promise that a schedule
// "pins the image reference... even if a newer image appears before
// then" (found while validating Schedule end-to-end).
//
// Best-effort: any failure (private registry, no network, rate limit)
// falls back to the plain currentImage, same as before this fix — a
// schedule should still be creatable even when digest resolution isn't
// available, just without the stronger pin guarantee.
func pinImage(ctx context.Context, currentImage string) string {
	if registry.IsDigestReference(currentImage) {
		return currentImage
	}
	ref := registry.ParseRef(currentImage)
	digest, err := registry.Digest(ctx, scheduleHTTPClient, ref)
	if err != nil {
		return currentImage
	}
	return stripImageTag(currentImage) + "@" + digest
}

// stripImageTag removes a trailing ":<tag>" from image, being careful not
// to mistake a "host:port/" prefix's colon for a tag separator (the tag
// colon, if any, is always in the last "/"-separated segment).
func stripImageTag(image string) string {
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return image[:colon]
	}
	return image
}

// scheduleRequest is the POST /api/v1/schedules body, matching the three
// variants documented in docs/api.md.
type scheduleRequest struct {
	Type      string `json:"type"` // "once" | "cron" | "relative"
	Container string `json:"container,omitempty"`
	Stack     string `json:"stack,omitempty"`
	RunAt     string `json:"run_at,omitempty"`
	Cron      string `json:"cron,omitempty"`
	Policy    string `json:"policy,omitempty"` // "immediate" | "delayed"
	After     string `json:"after,omitempty"`
	Semver    string `json:"semver,omitempty"` // "patch" | "minor" | "major"
	Notify    *bool  `json:"notify,omitempty"`
}

type scheduleResponse struct {
	ID          int64   `json:"id"`
	Type        string  `json:"type"`
	Container   string  `json:"container,omitempty"`
	Stack       string  `json:"stack,omitempty"`
	RunAt       *string `json:"run_at,omitempty"`
	Cron        string  `json:"cron,omitempty"`
	Policy      string  `json:"policy,omitempty"`
	After       string  `json:"after,omitempty"`
	Semver      string  `json:"semver,omitempty"`
	PinnedImage string  `json:"pinned_image,omitempty"`
	Notify      bool    `json:"notify"`
	Enabled     bool    `json:"enabled"`
	LastRunAt   *string `json:"last_run_at,omitempty"`
	NextRunAt   *string `json:"next_run_at,omitempty"`
}

func toScheduleResponse(sc store.Schedule) scheduleResponse {
	resp := scheduleResponse{
		ID: sc.ID, Type: sc.Kind, Container: sc.ContainerName, Stack: sc.Stack,
		Cron: sc.CronExpr, Policy: sc.RelativePolicy, After: sc.RelativeAfter,
		Semver:      sc.SemverPolicy,
		PinnedImage: sc.PinnedImage, Notify: sc.Notify, Enabled: sc.Enabled,
	}
	if sc.RunAt != nil {
		s := sc.RunAt.Format(time.RFC3339)
		resp.RunAt = &s
	}
	if sc.LastRunAt != nil {
		s := sc.LastRunAt.Format(time.RFC3339)
		resp.LastRunAt = &s
	}
	if sc.NextRunAt != nil {
		s := sc.NextRunAt.Format(time.RFC3339)
		resp.NextRunAt = &s
	}
	return resp
}

// handleListSchedules backs GET /api/v1/schedules.
func handleListSchedules(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schedules, err := st.ListSchedules(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to list schedules")
			return
		}
		resp := make([]scheduleResponse, 0, len(schedules))
		for _, sc := range schedules {
			resp = append(resp, toScheduleResponse(sc))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleCreateSchedule backs POST /api/v1/schedules — creates a one-off,
// cron, or relative schedule (docs/schedule.md). A one-off schedule pins
// the container's *current* image at creation time, applied verbatim at
// run_at even if a newer one appears before then (docs/containers.md).
func handleCreateSchedule(st *store.Store, notif *notifier.Notifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req scheduleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}

		notify := true
		if req.Notify != nil {
			notify = *req.Notify
		}
		sc := store.Schedule{Notify: notify, Enabled: true, SemverPolicy: req.Semver}
		if sc.SemverPolicy == "" {
			sc.SemverPolicy = "major"
		}
		if sc.SemverPolicy != "patch" && sc.SemverPolicy != "minor" && sc.SemverPolicy != "major" {
			writeError(w, http.StatusBadRequest, "invalid_request", `semver must be "patch", "minor", or "major"`)
			return
		}

		switch req.Type {
		case "once":
			if (req.Container == "" && req.Stack == "") || req.RunAt == "" {
				writeError(w, http.StatusBadRequest, "invalid_request", "once schedules require container or stack, and run_at")
				return
			}
			runAt, err := time.Parse(time.RFC3339, req.RunAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "run_at must be RFC3339")
				return
			}
			if runAt.Before(time.Now()) {
				writeError(w, http.StatusBadRequest, "invalid_request", "run_at must be in the future")
				return
			}

			if req.Stack != "" {
				containers, err := st.ListContainers(r.Context())
				if err != nil {
					writeError(w, http.StatusInternalServerError, "internal_error", "failed to list containers")
					return
				}
				resp := make([]scheduleResponse, 0)
				for _, rec := range containers {
					if rec.Stack != req.Stack {
						continue
					}
					member := store.Schedule{
						Notify: notify, Enabled: true,
						Kind: "once", ContainerName: rec.Name, RunAt: &runAt, NextRunAt: &runAt,
						PinnedImage: pinImage(r.Context(), rec.CurrentImage),
					}
					id, err := st.CreateSchedule(r.Context(), member)
					if err != nil {
						writeError(w, http.StatusInternalServerError, "internal_error", "failed to create schedule")
						return
					}
					member.ID = id
					resp = append(resp, toScheduleResponse(member))
				}
				if len(resp) == 0 {
					writeError(w, http.StatusNotFound, "not_found", "stack has no containers")
					return
				}
				if notify {
					notif.Notify(r.Context(), "schedule.created", "", req.Stack,
						fmt.Sprintf("once schedule created for %d container(s) in stack %s, running at %s", len(resp), req.Stack, req.RunAt))
				}
				writeJSON(w, http.StatusCreated, resp)
				return
			}

			rec, err := st.GetContainerByName(r.Context(), req.Container)
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "container not found")
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "failed to look up container")
				return
			}
			sc.Kind, sc.ContainerName, sc.RunAt, sc.NextRunAt = "once", req.Container, &runAt, &runAt
			sc.PinnedImage = pinImage(r.Context(), rec.CurrentImage)

		case "cron":
			if req.Cron == "" || (req.Container == "" && req.Stack == "") {
				writeError(w, http.StatusBadRequest, "invalid_request", "cron schedules require cron and container or stack")
				return
			}
			next, err := scheduler.NextCronRun(req.Cron)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			sc.Kind, sc.ContainerName, sc.Stack, sc.CronExpr, sc.NextRunAt = "cron", req.Container, req.Stack, req.Cron, next

		case "relative":
			if req.Policy != "immediate" && req.Policy != "delayed" {
				writeError(w, http.StatusBadRequest, "invalid_request", `policy must be "immediate" or "delayed"`)
				return
			}
			if req.Container == "" && req.Stack == "" {
				writeError(w, http.StatusBadRequest, "invalid_request", "relative schedules require container or stack")
				return
			}
			sc.Kind, sc.ContainerName, sc.Stack = "relative", req.Container, req.Stack
			sc.RelativePolicy, sc.RelativeAfter = req.Policy, req.After
			if req.Policy == "delayed" {
				delay, err := time.ParseDuration(req.After)
				if err != nil || delay <= 0 {
					writeError(w, http.StatusBadRequest, "invalid_request", "after must be a positive Go duration such as 24h")
					return
				}
			}

		default:
			writeError(w, http.StatusBadRequest, "invalid_request", `type must be "once", "cron", or "relative"`)
			return
		}

		id, err := st.CreateSchedule(r.Context(), sc)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to create schedule")
			return
		}
		sc.ID = id
		if notify {
			notif.Notify(r.Context(), "schedule.created", sc.ContainerName, sc.Stack, describeSchedule(sc))
		}
		writeJSON(w, http.StatusCreated, []scheduleResponse{toScheduleResponse(sc)})
	}
}

// describeSchedule renders a one-line human summary of sc for the
// schedule.created/modified/cancelled notification types.
func describeSchedule(sc store.Schedule) string {
	target := sc.ContainerName
	if target == "" {
		target = "stack " + sc.Stack
	}
	switch sc.Kind {
	case "once":
		when := ""
		if sc.RunAt != nil {
			when = sc.RunAt.Format(time.RFC3339)
		}
		return fmt.Sprintf("once schedule #%d for %s, running at %s", sc.ID, target, when)
	case "cron":
		return fmt.Sprintf("cron schedule #%d for %s: %s (%s updates)", sc.ID, target, sc.CronExpr, sc.SemverPolicy)
	case "relative":
		return fmt.Sprintf("relative/%s schedule #%d for %s (%s updates)", sc.RelativePolicy, sc.ID, target, sc.SemverPolicy)
	default:
		return fmt.Sprintf("schedule #%d for %s", sc.ID, target)
	}
}

// scheduleUpdateRequest is the PATCH /api/v1/schedules/{id} body. Only
// `enabled` is supported today — toggling a schedule on/off, e.g. from
// the Schedule page's enabled/disabled control.
type scheduleUpdateRequest struct {
	Enabled *bool `json:"enabled"`
}

// handleUpdateSchedule backs PATCH /api/v1/schedules/{id}.
func handleUpdateSchedule(st *store.Store, notif *notifier.Notifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid schedule id")
			return
		}

		var req scheduleUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
		if req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "enabled is required")
			return
		}
		existing, err := st.GetSchedule(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "schedule not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load schedule")
			return
		}
		if existing.Completed() {
			writeError(w, http.StatusConflict, "schedule_completed", "a completed once schedule cannot be enabled or disabled")
			return
		}

		if err := st.SetScheduleEnabled(r.Context(), id, *req.Enabled); errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "schedule not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to update schedule")
			return
		}
		// Best-effort lookup purely for the notification's target/message —
		// the update itself already succeeded above regardless of this.
		if sc, err := st.GetSchedule(r.Context(), id); err == nil && sc.Notify {
			state := "disabled"
			if *req.Enabled {
				state = "enabled"
			}
			notif.Notify(r.Context(), "schedule.modified", sc.ContainerName, sc.Stack,
				fmt.Sprintf("%s %s", describeSchedule(sc), state))
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// handleDeleteSchedule backs DELETE /api/v1/schedules/{id}.
func handleDeleteSchedule(st *store.Store, notif *notifier.Notifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid schedule id")
			return
		}
		// Looked up before deleting so the cancellation notification can
		// still name the container/stack it was for — DeleteSchedule
		// itself doesn't return the row it removed.
		sc, lookupErr := st.GetSchedule(r.Context(), id)
		if err := st.DeleteSchedule(r.Context(), id); errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "schedule not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete schedule")
			return
		}
		if lookupErr == nil && sc.Notify && !sc.Completed() {
			notif.Notify(r.Context(), "schedule.cancelled", sc.ContainerName, sc.Stack, describeSchedule(sc)+" cancelled")
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
