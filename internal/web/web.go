// Package web serves the embedded web UI (html/template + go:embed of
// web/templates and web/static), mounted alongside the API on the same
// HTTP server (see docs/architecture.md). Only the Containers page — the
// default landing page per docs/web-ui.md — is implemented so far; the
// other sidebar sections are listed but disabled until their subsystems
// exist.
package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strings"

	"github.com/arnaudcharles/doupro/internal/auth"
	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/registry"
	"github.com/arnaudcharles/doupro/internal/store"
	"github.com/arnaudcharles/doupro/internal/updater"
)

// RegisterRoutes mounts the web UI and its static assets onto mux.
// templatesFS and staticFS are the embed.FS values declared at the module
// root (see /assets.go) — go:embed can't reach web/templates or
// web/static directly from this package's own directory.
func RegisterRoutes(mux *http.ServeMux, st *store.Store, upd *updater.Updater, notif *notifier.Notifier, version string, templatesFS fs.FS, staticFS fs.FS, manualsFS fs.FS, oidcEnabled bool) {
	tpl := template.Must(template.ParseFS(templatesFS, "web/templates/*.html"))

	static, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		panic(err) // embedded at build time, a failure here is a build bug
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	mux.HandleFunc("GET /login", handleLoginPage(tpl, oidcEnabled))

	protected := auth.RequireAuth(st)
	withPermission := func(permission store.Permission, handler http.Handler) http.Handler {
		return protected(auth.RequirePermission(permission)(handler))
	}
	mux.Handle("GET /change-password", protected(handleChangePasswordPage(tpl)))
	mux.Handle("GET /{$}", withPermission(store.PermissionReadContainers, handleContainers(st, upd, tpl, version)))
	mux.Handle("GET /logs", withPermission(store.PermissionViewLogs, handleLogs(st, tpl, version)))
	mux.Handle("GET /schedule", withPermission(store.PermissionManageSchedules, handleSchedule(st, tpl, version)))
	mux.Handle("GET /notifications", withPermission(store.PermissionManageNotifications, handleNotifications(st, notif, tpl, version)))
	mux.Handle("GET /settings", withPermission(store.PermissionManageSettings, handleSettings(st, tpl, version)))
	mux.Handle("GET /stats", withPermission(store.PermissionViewStats, handleStats(st, tpl, version)))
	mux.Handle("GET /manual", withPermission(store.PermissionReadContainers, handleManual(manualsFS, tpl, version)))
	mux.Handle("GET /manual/{page}", withPermission(store.PermissionReadContainers, handleManual(manualsFS, tpl, version)))
	mux.Handle("GET /swagger", protected(handleSwagger(tpl)))
}

// handleSwagger serves a self-contained Swagger UI (vendored under
// web/static/swagger/, no CDN — see SECURITY.md) pointed at
// /api/v1/openapi.json. It renders its own full HTML document rather than
// going through "layout", since Swagger UI needs to own the whole page.
func handleSwagger(tpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := struct{ CSPNonce string }{CSPNonce: auth.CSPNonceFromContext(r.Context())}
		if err := tpl.ExecuteTemplate(w, "swagger-page", data); err != nil {
			http.Error(w, "render error", http.StatusInternalServerError)
		}
	}
}

// handleChangePasswordPage serves the standalone change-password form.
// Reachable both as the forced first-login step (RequireAuth redirects
// here for any must_change_password account) and by an operator who just
// navigates to it directly.
func handleChangePasswordPage(tpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := struct{ CSPNonce string }{CSPNonce: auth.CSPNonceFromContext(r.Context())}
		if err := tpl.ExecuteTemplate(w, "change-password-page", data); err != nil {
			http.Error(w, "render error", http.StatusInternalServerError)
		}
	}
}

func handleLoginPage(tpl *template.Template, oidcEnabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := struct {
			Error       bool
			OIDCEnabled bool
			CSPNonce    string
		}{Error: r.URL.Query().Get("error") != "", OIDCEnabled: oidcEnabled, CSPNonce: auth.CSPNonceFromContext(r.Context())}
		if err := tpl.ExecuteTemplate(w, "login-page", data); err != nil {
			http.Error(w, "render error", http.StatusInternalServerError)
		}
	}
}

type navItem struct {
	Label   string
	Href    string
	Enabled bool
	Active  bool
}

// sidebarSections mirrors the sidebar order documented in docs/web-ui.md.
// Sections without a route render disabled with a "soon" badge instead of
// linking to a page that doesn't exist yet.
var sidebarSections = []struct {
	label string
	href  string
}{
	{"Containers", "/"},
	{"Schedule", "/schedule"},
	{"Notifications", "/notifications"},
	{"Logs", "/logs"},
	{"Settings", "/settings"},
	{"Stats", "/stats"},
	{"Manual", "/manual"},
}

func mustAccess(r *http.Request) store.AccessControl {
	access, _ := auth.AccessFromContext(r.Context())
	return access
}

func navItems(active string, access store.AccessControl) []navItem {
	out := make([]navItem, 0, len(sidebarSections))
	for _, s := range sidebarSections {
		allowed := true
		switch s.label {
		case "Logs":
			allowed = access.Can(store.PermissionViewLogs)
		case "Schedule":
			allowed = access.Can(store.PermissionManageSchedules)
		case "Notifications":
			allowed = access.Can(store.PermissionManageNotifications)
		case "Settings":
			allowed = access.Can(store.PermissionManageSettings)
		case "Stats":
			allowed = access.Can(store.PermissionViewStats)
		}
		if !allowed {
			continue
		}
		out = append(out, navItem{
			Label:   s.label,
			Href:    s.href,
			Enabled: s.href != "",
			Active:  s.label == active,
		})
	}
	return out
}

// containerView is the display model for one container row — derived from
// store.ContainerRecord, not a passthrough, so template logic stays out of
// the templates themselves.
type containerView struct {
	Name       string
	StateClass string // running|warning|created|paused|restarting|removing|exited|dead|stopped — drives color and icon
	StateLabel string // the real Docker state name, upper-cased (see stateLabel)

	// DefinedImage is the raw compose-defined reference (e.g.
	// "nginx:latest") — shown small/grey next to the name, context rather
	// than the headline.
	DefinedImage string

	// ResolvedVersion is what the version chip shows. When VersionResolved
	// is true, it is an exact publisher version. Otherwise it is the source
	// registry tag (or immutable short digest for a digest-only reference),
	// so every registry-backed row still has a useful identity without
	// presenting a floating tag as semantic-version precision.
	ResolvedVersion string
	VersionResolved bool

	// ResolvedDigestHint is the shortened immutable digest behind a tag
	// fallback, shown in that chip's tooltip.
	ResolvedDigestHint string

	// AvailableVersion is the resolved version of the update, if known —
	// empty when UpdateAvailable but unresolved (a digest is dropped the
	// same way, never shown as if it were a version), in which case the
	// template shows a plain "Update available" with no version number.
	AvailableVersion string
	UpdateKind       string // version | rebuild when UpdateAvailable is true

	// HasPrevious drives the Rollback button's enabled state. RollbackHint
	// is the shortened previous image, tooltip-only (see shortImageRef) —
	// the raw digest isn't shown as visible chip text on the Containers
	// page (truncated and unreadable, added no information beyond the
	// events log, which already records it on every update/rollback), but
	// still worth surfacing on hover so clicking Rollback again (always
	// available — it's self-reversible, same as Update) isn't a total
	// guess about what it switches to.
	HasPrevious     bool
	RollbackHint    string
	UpdateAvailable bool
	Excluded        bool
	Operation       string
	CrashCount      int
	CrashThreshold  int
	RollbackWatched bool
}

// shortImageRef shortens a raw image ID (e.g. "sha256:<64 hex chars>",
// what previous_image is stored as — see docs/containers.md) to something
// that fits a chip. Tag-based references are returned unchanged.
func shortImageRef(ref string) string {
	if strings.HasPrefix(ref, "sha256:") && len(ref) > 19 {
		return ref[:19] + "…"
	}
	return ref
}

func versionDisplay(currentImage, currentVersion string) (label string, exact bool, digestHint string) {
	if currentVersion != "" && !strings.HasPrefix(currentVersion, "sha256:") {
		return currentVersion, true, ""
	}
	if strings.HasPrefix(currentVersion, "sha256:") {
		digestHint = shortImageRef(currentVersion)
	}
	if currentImage != "" && !registry.IsDigestReference(currentImage) {
		if tag := registry.ParseRef(currentImage).Tag; tag != "" {
			return tag, false, digestHint
		}
	}
	if digestHint != "" {
		return digestHint, false, digestHint
	}
	if currentImage != "" {
		return shortImageRef(currentImage), false, ""
	}
	return "No image reference", false, ""
}

type stackView struct {
	Name       string
	StackParam string // the real store.ContainerRecord.Stack value ("" for the Ungrouped pseudo-stack), for API calls scoped by stack
	Containers []containerView
}

// pageData is the shared shell data every page passes to "layout". Body is
// the page's content, pre-rendered by renderPage (html/template's
// {{template "name" pipeline}} action requires a string literal name, so
// dynamic per-page content is rendered in Go instead — see renderPage).
// Page-specific fields (Total/Stacks for Containers, Logs/Filter/Limit for
// Logs) are only populated by the handler that needs them.
type pageData struct {
	Title              string
	Version            string
	Nav                []navItem
	Body               template.HTML
	Error              string
	Access             store.AccessControl
	CanWriteContainers bool
	CanManageSchedules bool
	CSPNonce           string

	// Containers page
	Total  int
	Stacks []stackView

	// Logs page
	Logs      []logView
	Filter    logFilterView
	Limit     int
	ExportURL string

	// Schedule page — Schedules is the flat list (used for the total
	// count only); ContainerSchedules/RecurringSchedules are the same
	// records split per docs/schedule.md's two-list model ("Individual
	// schedules (per container)" vs "Recurring policies (per stack or
	// container group)") for display.
	Schedules          []scheduleView
	ContainerSchedules []scheduleView
	RecurringSchedules []scheduleView
	PrefillContainer   string

	// Notifications page
	Notifications    []notificationView
	NotificationJobs []notificationJobView
	Channels         []notifier.Channel
	ChannelsJSON     template.JS

	// Settings page
	CheckIntervalMinutes   int
	CrashLoopThreshold     int
	CrashLoopWindowMinutes int
	LogsPageSize           int
	MinLogsPageSize        int
	MaxLogsPageSize        int
	ExcludedContainersCSV  string
	ExcludedStacksCSV      string
	APIKeys                []apiKeyView
	Users                  []userView
	CurrentUserID          int64
	Registries             []registryView

	// Stats page
	Stats store.StatsSummary

	// Manual page
	ManualPages []manualPage
	ManualSlug  string
	ManualHTML  template.HTML
}

// renderPage executes the named content template into data.Body, then
// renders the shared "layout" template around it.
func renderPage(w http.ResponseWriter, r *http.Request, tpl *template.Template, contentTemplate string, data pageData) {
	data.CSPNonce = auth.CSPNonceFromContext(r.Context())

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, contentTemplate, data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	data.Body = template.HTML(buf.String()) //nolint:gosec // our own compiled templates, not user input

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func handleContainers(st *store.Store, upd *updater.Updater, tpl *template.Template, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		records, err := st.ListContainers(r.Context())
		if err != nil {
			http.Error(w, "failed to load containers", http.StatusInternalServerError)
			return
		}

		grouped := make(map[string][]containerView)
		for _, c := range records {
			stack := c.Stack
			if stack == "" {
				stack = "Ungrouped"
			}
			class := classifyState(c.State, c.Status)

			resolvedVersion, versionResolved, digestHint := versionDisplay(c.CurrentImage, c.CurrentVersion)

			availableVersion := c.AvailableVersion
			if strings.HasPrefix(availableVersion, "sha256:") {
				availableVersion = ""
			}

			operation := ""
			if op, ok := upd.Operation(c.Name); ok {
				operation = op.Kind
			}
			watch, watchErr := st.GetRollbackWatch(r.Context(), c.Name)
			watched := watchErr == nil && watch.ContainerID == c.ID && (watch.Status == "active" || watch.Status == "triggered")
			grouped[stack] = append(grouped[stack], containerView{
				Name:               c.Name,
				StateClass:         class,
				StateLabel:         stateLabel(class),
				DefinedImage:       c.CurrentImage,
				ResolvedVersion:    resolvedVersion,
				VersionResolved:    versionResolved,
				ResolvedDigestHint: digestHint,
				AvailableVersion:   availableVersion,
				UpdateKind:         registry.ClassifyUpdate(c.CurrentVersion, c.AvailableVersion, c.UpdateAvailable),
				HasPrevious:        c.PreviousImage != "",
				RollbackHint:       shortImageRef(c.PreviousImage),
				UpdateAvailable:    c.UpdateAvailable,
				Excluded:           c.Excluded,
				Operation:          operation,
				RollbackWatched:    watched,
				CrashCount:         watch.CrashCount,
				CrashThreshold:     watch.Threshold,
			})
		}

		names := make([]string, 0, len(grouped))
		for name := range grouped {
			names = append(names, name)
		}
		sort.Strings(names)

		stacks := make([]stackView, 0, len(names))
		for _, name := range names {
			param := name
			if name == "Ungrouped" {
				param = ""
			}
			stacks = append(stacks, stackView{Name: name, StackParam: param, Containers: grouped[name]})
		}

		data := pageData{
			Title:              "Containers",
			Version:            version,
			Nav:                navItems("Containers", mustAccess(r)),
			Access:             mustAccess(r),
			CanWriteContainers: mustAccess(r).Can(store.PermissionWriteContainers),
			CanManageSchedules: mustAccess(r).Can(store.PermissionManageSchedules),
			Error:              r.URL.Query().Get("error"),
			Total:              len(records),
			Stacks:             stacks,
		}

		renderPage(w, r, tpl, "containers-content", data)
	}
}

// classifyState maps a raw Docker container state to a display class per
// the color convention in docs/containers.md. Docker itself only emits
// created/restarting/running/removing/paused/exited/dead — "warning" is
// the one class not a literal Docker state, reserved for a running
// container whose health check has gone unhealthy (status carries the
// "(unhealthy)" suffix Docker appends, e.g. "Up 3 hours (unhealthy)").
func classifyState(state, status string) string {
	switch strings.ToLower(state) {
	case "running":
		if strings.Contains(strings.ToLower(status), "(unhealthy)") {
			return "warning"
		}
		return "running"
	case "restarting":
		return "restarting"
	case "removing":
		return "removing"
	case "paused":
		return "paused"
	case "created":
		return "created"
	case "exited":
		return "exited"
	case "dead":
		return "dead"
	default:
		return "stopped"
	}
}

// stateLabel renders the true state name as the badge text, uppercased —
// see docs/containers.md for the color each class maps to.
func stateLabel(class string) string {
	switch class {
	case "warning":
		return "WARNING"
	case "stopped":
		return "STOPPED"
	default:
		return strings.ToUpper(class)
	}
}
