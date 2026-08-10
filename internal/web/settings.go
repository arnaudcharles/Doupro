package web

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/arnaudcharles/doupro/internal/auth"
	"github.com/arnaudcharles/doupro/internal/scheduler"
	"github.com/arnaudcharles/doupro/internal/store"
)

type apiKeyView struct {
	ID        int64
	Name      string
	KeyPrefix string
	CreatedAt string
	Access    store.AccessControl
}

type userView struct {
	ID           int64
	Username     string
	AuthProvider string
	CreatedAt    string
	Access       store.AccessControl
}

type registryView struct {
	Host, Username, AuthHost                             string
	CredentialsConfigured, ProxyConfigured, CAConfigured bool
}

func handleSettings(st *store.Store, tpl *template.Template, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		minutes, ok, err := st.GetCheckIntervalMinutes(r.Context())
		if err != nil {
			http.Error(w, "failed to load settings", http.StatusInternalServerError)
			return
		}
		if !ok {
			minutes = int(scheduler.DefaultCheckInterval / time.Minute)
		}
		crashThreshold, crashWindowMinutes, err := st.GetCrashLoopSettings(r.Context())
		if err != nil {
			http.Error(w, "failed to load crash-loop settings", http.StatusInternalServerError)
			return
		}
		logsPageSize, err := st.GetLogsPageSize(r.Context())
		if err != nil {
			http.Error(w, "failed to load logs settings", http.StatusInternalServerError)
			return
		}

		exclusions, err := st.GetExclusions(r.Context())
		if err != nil {
			http.Error(w, "failed to load exclusions", http.StatusInternalServerError)
			return
		}
		registryConfigs, err := st.GetRegistryConfigs(r.Context())
		if err != nil {
			http.Error(w, "failed to load registries", 500)
			return
		}
		registryViews := make([]registryView, 0, len(registryConfigs))
		for _, cfg := range registryConfigs {
			registryViews = append(registryViews, registryView{Host: cfg.Host, Username: cfg.Username, AuthHost: cfg.AuthHost,
				CredentialsConfigured: cfg.Username != "" && cfg.Password != "", ProxyConfigured: cfg.ProxyURL != "", CAConfigured: cfg.CACertPEM != ""})
		}

		keys, err := st.ListAPIKeys(r.Context())
		if err != nil {
			http.Error(w, "failed to load api keys", http.StatusInternalServerError)
			return
		}
		keyViews := make([]apiKeyView, 0, len(keys))
		for _, k := range keys {
			keyViews = append(keyViews, apiKeyView{
				ID: k.ID, Name: k.Name, KeyPrefix: k.KeyPrefix, CreatedAt: k.CreatedAt.Format("2006-01-02"), Access: k.Access,
			})
		}

		users, err := st.ListUsers(r.Context())
		if err != nil {
			http.Error(w, "failed to load users", http.StatusInternalServerError)
			return
		}
		userViews := make([]userView, 0, len(users))
		for _, u := range users {
			userViews = append(userViews, userView{ID: u.ID, Username: u.Username, AuthProvider: u.AuthProvider,
				CreatedAt: u.CreatedAt.Format("2006-01-02"), Access: u.Access})
		}
		var currentUserID int64
		if current, ok := auth.UserFromContext(r.Context()); ok {
			currentUserID = current.ID
		}

		data := pageData{
			Title:                  "Settings",
			Version:                version,
			Nav:                    navItems("Settings", mustAccess(r)),
			CheckIntervalMinutes:   minutes,
			CrashLoopThreshold:     crashThreshold,
			CrashLoopWindowMinutes: crashWindowMinutes,
			LogsPageSize:           logsPageSize,
			MinLogsPageSize:        store.MinLogsPageSize,
			MaxLogsPageSize:        store.MaxLogsPageSize,
			ExcludedContainersCSV:  strings.Join(exclusions.Containers, ", "),
			ExcludedStacksCSV:      strings.Join(exclusions.Stacks, ", "),
			APIKeys:                keyViews,
			Users:                  userViews,
			CurrentUserID:          currentUserID,
			Registries:             registryViews,
		}
		renderPage(w, r, tpl, "settings-content", data)
	}
}
