package cliclient

import (
	"context"
	"fmt"
	"net/url"
)

type Access struct {
	Role                string `json:"role"`
	ViewLogs            bool   `json:"view_logs"`
	ManageSchedules     bool   `json:"manage_schedules"`
	ManageNotifications bool   `json:"manage_notifications"`
	ViewStats           bool   `json:"view_stats"`
}

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	AuthProvider string `json:"auth_provider"`
	CreatedAt    string `json:"created_at"`
	Access       Access `json:"access"`
}

func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	var users []User
	err := c.do(ctx, "GET", "/api/v1/users", nil, nil, &users)
	return users, err
}

func (c *Client) CreateUser(ctx context.Context, username, password string, access Access) (User, error) {
	var user User
	err := c.do(ctx, "POST", "/api/v1/users", nil, map[string]any{"username": username, "password": password, "access": access}, &user)
	return user, err
}

func (c *Client) SetUserAccess(ctx context.Context, id string, access Access) error {
	return c.do(ctx, "PATCH", "/api/v1/users/"+url.PathEscape(id), nil, access, nil)
}

func (c *Client) DeleteUser(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/api/v1/users/"+url.PathEscape(id), nil, nil, nil)
}

type APIKey struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	KeyPrefix  string `json:"key_prefix"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	Key        string `json:"key,omitempty"`
	Access     Access `json:"access"`
}

func (c *Client) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	var keys []APIKey
	err := c.do(ctx, "GET", "/api/v1/settings/security/api-keys", nil, nil, &keys)
	return keys, err
}

func (c *Client) CreateAPIKey(ctx context.Context, name string, access Access) (APIKey, error) {
	var key APIKey
	err := c.do(ctx, "POST", "/api/v1/settings/security/api-keys", nil, map[string]any{"name": name, "access": access}, &key)
	return key, err
}

func (c *Client) RevokeAPIKey(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/api/v1/settings/security/api-keys/"+url.PathEscape(id), nil, nil, nil)
}

// Container mirrors internal/api's containerResponse (docs/api.md).
type Container struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Stack            string              `json:"stack"`
	State            string              `json:"state"`
	Status           string              `json:"status"`
	CurrentImage     string              `json:"current_image"`
	PreviousImage    string              `json:"previous_image"`
	CurrentVersion   string              `json:"current_version,omitempty"`
	CurrentTag       string              `json:"current_tag,omitempty"`
	VersionSource    string              `json:"version_source"`
	AvailableVersion string              `json:"available_version,omitempty"`
	UpdateKind       string              `json:"update_kind,omitempty"`
	UpdateAvailable  bool                `json:"update_available"`
	Excluded         bool                `json:"excluded"`
	LastCheckedAt    string              `json:"last_checked_at"`
	Operation        *ContainerOperation `json:"operation,omitempty"`
	RollbackWatch    *RollbackWatch      `json:"rollback_watch,omitempty"`
}

type RollbackWatch struct {
	CrashCount      int    `json:"crash_count"`
	Threshold       int    `json:"threshold"`
	WindowExpiresAt string `json:"window_expires_at"`
	Status          string `json:"status"`
}

type ContainerOperation struct {
	Kind      string `json:"kind"`
	StartedAt string `json:"started_at"`
}

// ListContainers returns every tracked container, optionally filtered to
// one stack client-side (the API doesn't support server-side filtering
// yet).
func (c *Client) ListContainers(ctx context.Context, stack string) ([]Container, error) {
	var containers []Container
	if err := c.do(ctx, "GET", "/api/v1/containers", nil, nil, &containers); err != nil {
		return nil, err
	}
	if stack == "" {
		return containers, nil
	}
	filtered := containers[:0]
	for _, cont := range containers {
		if cont.Stack == stack {
			filtered = append(filtered, cont)
		}
	}
	return filtered, nil
}

// UpdateContainer triggers an update for the named container.
// GetContainer returns full detail for one tracked container by name.
func (c *Client) GetContainer(ctx context.Context, name string) (Container, error) {
	var cont Container
	err := c.do(ctx, "GET", "/api/v1/containers/"+url.PathEscape(name), nil, nil, &cont)
	return cont, err
}

// CheckResult is the outcome of CheckContainer.
type CheckResult struct {
	Checked         bool `json:"checked"`
	UpdateAvailable bool `json:"update_available"`
}

// CheckContainer forces an immediate registry check for one container,
// outside the periodic check cycle.
func (c *Client) CheckContainer(ctx context.Context, name string) (CheckResult, error) {
	var result CheckResult
	err := c.do(ctx, "POST", "/api/v1/containers/"+url.PathEscape(name)+"/check", nil, nil, &result)
	return result, err
}

func (c *Client) UpdateContainer(ctx context.Context, name, semverPolicy string) error {
	q := url.Values{}
	if semverPolicy != "" {
		q.Set("semver", semverPolicy)
	}
	return c.do(ctx, "POST", "/api/v1/containers/"+url.PathEscape(name)+"/update", q, nil, nil)
}

// UpdatePreview mirrors internal/api's updatePreviewResponse.
type UpdatePreview struct {
	CurrentImage    string `json:"current_image"`
	CurrentVersion  string `json:"current_version,omitempty"`
	TargetImage     string `json:"target_image"`
	TargetVersion   string `json:"target_version,omitempty"`
	UpdateKind      string `json:"update_kind,omitempty"`
	SemverScope     string `json:"semver_scope"`
	UpdateAvailable bool   `json:"update_available"`
}

// PreviewUpdate is the read-only dry-run counterpart to UpdateContainer:
// resolves what an update at semverPolicy would target, without pulling
// or touching Docker at all.
func (c *Client) PreviewUpdate(ctx context.Context, name, semverPolicy string) (UpdatePreview, error) {
	q := url.Values{}
	if semverPolicy != "" {
		q.Set("semver", semverPolicy)
	}
	var preview UpdatePreview
	err := c.do(ctx, "GET", "/api/v1/containers/"+url.PathEscape(name)+"/update", q, nil, &preview)
	return preview, err
}

// RollbackContainer triggers a rollback for the named container.
func (c *Client) RollbackContainer(ctx context.Context, name string) error {
	return c.do(ctx, "POST", "/api/v1/containers/"+url.PathEscape(name)+"/rollback", nil, nil, nil)
}

// UpdateAllContainers triggers Update for every container that currently
// has an update available (excluded containers and DoUpRo itself are
// always skipped server-side), optionally scoped to one stack. Returns how
// many were queued — the updates themselves run in the background.
func (c *Client) UpdateAllContainers(ctx context.Context, stack string) (int, error) {
	q := url.Values{}
	if stack != "" {
		q.Set("stack", stack)
	}
	var resp struct {
		Queued int `json:"queued"`
	}
	err := c.do(ctx, "POST", "/api/v1/containers/update-all", q, nil, &resp)
	return resp.Queued, err
}

// LogEntry mirrors internal/api's logResponse (docs/api.md).
type LogEntry struct {
	ID          int64          `json:"id"`
	Timestamp   string         `json:"timestamp"`
	Level       string         `json:"level"`
	Event       string         `json:"event"`
	Container   string         `json:"container"`
	Stack       string         `json:"stack"`
	FromVersion string         `json:"from_version"`
	ToVersion   string         `json:"to_version"`
	Actor       string         `json:"actor"`
	ActorID     string         `json:"actor_id"`
	Message     string         `json:"message"`
	Metadata    map[string]any `json:"metadata"`
}

// LogFilter narrows ListLogs — zero values mean "no filter".
type LogFilter struct {
	Event     string
	Container string
	Level     string
	Limit     int
}

func (c *Client) ListLogs(ctx context.Context, f LogFilter) ([]LogEntry, error) {
	q := url.Values{}
	if f.Event != "" {
		q.Set("event", f.Event)
	}
	if f.Container != "" {
		q.Set("container", f.Container)
	}
	if f.Level != "" {
		q.Set("level", f.Level)
	}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", f.Limit))
	}
	var logs []LogEntry
	if err := c.do(ctx, "GET", "/api/v1/logs", q, nil, &logs); err != nil {
		return nil, err
	}
	return logs, nil
}

// Schedule mirrors internal/api's scheduleResponse (docs/api.md).
type Schedule struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Container   string `json:"container"`
	Stack       string `json:"stack"`
	RunAt       string `json:"run_at"`
	Cron        string `json:"cron"`
	Policy      string `json:"policy"`
	After       string `json:"after"`
	Semver      string `json:"semver"`
	PinnedImage string `json:"pinned_image"`
	Notify      bool   `json:"notify"`
	Enabled     bool   `json:"enabled"`
	LastRunAt   string `json:"last_run_at"`
	NextRunAt   string `json:"next_run_at"`
}

func (c *Client) ListSchedules(ctx context.Context) ([]Schedule, error) {
	var schedules []Schedule
	if err := c.do(ctx, "GET", "/api/v1/schedules", nil, nil, &schedules); err != nil {
		return nil, err
	}
	return schedules, nil
}

// CreateScheduleRequest mirrors internal/api's scheduleRequest.
type CreateScheduleRequest struct {
	Type      string `json:"type"`
	Container string `json:"container,omitempty"`
	Stack     string `json:"stack,omitempty"`
	RunAt     string `json:"run_at,omitempty"`
	Cron      string `json:"cron,omitempty"`
	Policy    string `json:"policy,omitempty"`
	After     string `json:"after,omitempty"`
	Semver    string `json:"semver,omitempty"`
}

// CreateSchedule returns one schedule per request, except when targeting a
// stack with type "once": the API fans that out into one schedule per
// container in the stack.
func (c *Client) CreateSchedule(ctx context.Context, req CreateScheduleRequest) ([]Schedule, error) {
	var schedules []Schedule
	if err := c.do(ctx, "POST", "/api/v1/schedules", nil, req, &schedules); err != nil {
		return nil, err
	}
	return schedules, nil
}

func (c *Client) DeleteSchedule(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/api/v1/schedules/"+url.PathEscape(id), nil, nil, nil)
}

// SetScheduleEnabled toggles a schedule on/off — the same
// PATCH /api/v1/schedules/{id} the web UI's enabled/disabled button uses.
func (c *Client) SetScheduleEnabled(ctx context.Context, id string, enabled bool) error {
	return c.do(ctx, "PATCH", "/api/v1/schedules/"+url.PathEscape(id), nil, map[string]bool{"enabled": enabled}, nil)
}

// Notification mirrors internal/api's notificationResponse.
type Notification struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
	Target    string `json:"target"`
	Channel   string `json:"channel"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	Message   string `json:"message"`
}

func (c *Client) ListNotifications(ctx context.Context) ([]Notification, error) {
	var notifications []Notification
	if err := c.do(ctx, "GET", "/api/v1/notifications", nil, nil, &notifications); err != nil {
		return nil, err
	}
	return notifications, nil
}

type NotificationJob struct {
	ID            int64  `json:"id"`
	Event         string `json:"event"`
	Target        string `json:"target"`
	Channel       string `json:"channel"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	MaxAttempts   int    `json:"max_attempts"`
	NextAttemptAt string `json:"next_attempt_at"`
	LastError     string `json:"last_error"`
	Message       string `json:"message"`
}

func (c *Client) ListNotificationQueue(ctx context.Context) ([]NotificationJob, error) {
	var jobs []NotificationJob
	err := c.do(ctx, "GET", "/api/v1/notifications/queue", nil, nil, &jobs)
	return jobs, err
}

func (c *Client) RetryNotification(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/api/v1/notifications/queue/"+url.PathEscape(id)+"/retry", nil, nil, nil)
}

type NotificationChannel struct {
	Name       string   `json:"name"`
	Provider   string   `json:"provider"`
	Configured bool     `json:"configured"`
	Events     []string `json:"events"`
	Stacks     []string `json:"stacks"`
	Containers []string `json:"containers"`
}

func (c *Client) ListNotificationChannels(ctx context.Context) ([]NotificationChannel, error) {
	var channels []NotificationChannel
	err := c.do(ctx, "GET", "/api/v1/settings/notifications", nil, nil, &channels)
	return channels, err
}

// Stats mirrors internal/api's statsSummaryResponse.
type Stats struct {
	ContainersTotal     int            `json:"containers_total"`
	ContainersRunning   int            `json:"containers_running"`
	UpdatesAvailable    int            `json:"updates_available"`
	UpdatesSucceeded    int            `json:"updates_succeeded"`
	UpdatesFailed       int            `json:"updates_failed"`
	RollbacksAuto       int            `json:"rollbacks_auto"`
	RollbacksManual     int            `json:"rollbacks_manual"`
	NotificationsSent   int            `json:"notifications_sent"`
	NotificationsFailed int            `json:"notifications_failed"`
	SchedulesActive     int            `json:"schedules_active"`
	ContainersByStack   map[string]int `json:"containers_by_stack"`
}

func (c *Client) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	if err := c.do(ctx, "GET", "/api/v1/stats", nil, nil, &s); err != nil {
		return Stats{}, err
	}
	return s, nil
}

// GeneralSettings mirrors internal/api's generalSettingsResponse.
type GeneralSettings struct {
	CheckIntervalMinutes   int `json:"check_interval_minutes"`
	CrashLoopThreshold     int `json:"crash_loop_threshold"`
	CrashLoopWindowMinutes int `json:"crash_loop_window_minutes"`
	LogsPageSize           int `json:"logs_page_size"`
}

type RegistryConfig struct {
	Host                  string `json:"host"`
	Username              string `json:"username,omitempty"`
	AuthHost              string `json:"auth_host,omitempty"`
	Password              string `json:"password,omitempty"`
	ProxyURL              string `json:"proxy_url,omitempty"`
	CACertPEM             string `json:"ca_pem,omitempty"`
	CredentialsConfigured bool   `json:"credentials_configured,omitempty"`
	ProxyConfigured       bool   `json:"proxy_configured,omitempty"`
	CAConfigured          bool   `json:"ca_configured,omitempty"`
}

func (c *Client) ListRegistryConfigs(ctx context.Context) ([]RegistryConfig, error) {
	var configs []RegistryConfig
	err := c.do(ctx, "GET", "/api/v1/settings/registries", nil, nil, &configs)
	return configs, err
}

func (c *Client) SetRegistryConfigs(ctx context.Context, configs []RegistryConfig) error {
	return c.do(ctx, "PATCH", "/api/v1/settings/registries", nil, configs, nil)
}

func (c *Client) TestRegistryConfig(ctx context.Context, config RegistryConfig) error {
	return c.do(ctx, "POST", "/api/v1/settings/registries/test", nil, config, nil)
}

func (c *Client) GetGeneralSettings(ctx context.Context) (GeneralSettings, error) {
	var s GeneralSettings
	if err := c.do(ctx, "GET", "/api/v1/settings/general", nil, nil, &s); err != nil {
		return GeneralSettings{}, err
	}
	return s, nil
}

func (c *Client) SetGeneralSettings(ctx context.Context, s GeneralSettings) error {
	return c.do(ctx, "PATCH", "/api/v1/settings/general", nil, s, nil)
}
