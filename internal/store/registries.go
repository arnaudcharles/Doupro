package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const registriesSettingsKey = "registries.configs"

type RegistryConfig struct {
	Host      string `json:"host"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	AuthHost  string `json:"auth_host,omitempty"`
	ProxyURL  string `json:"proxy_url,omitempty"`
	CACertPEM string `json:"ca_pem,omitempty"`
}

func (s *Store) GetRegistryConfigs(ctx context.Context) ([]RegistryConfig, error) {
	raw, ok, err := s.GetSecretSetting(ctx, registriesSettingsKey)
	if err != nil || !ok {
		return nil, err
	}
	var configs []RegistryConfig
	if err := json.Unmarshal([]byte(raw), &configs); err != nil {
		return nil, fmt.Errorf("parse registry configs: %w", err)
	}
	return configs, nil
}

func (s *Store) SetRegistryConfigs(ctx context.Context, configs []RegistryConfig) error {
	existing, err := s.GetRegistryConfigs(ctx)
	if err != nil {
		return err
	}
	byHost := map[string]RegistryConfig{}
	for _, cfg := range existing {
		byHost[strings.ToLower(cfg.Host)] = cfg
	}
	for i := range configs {
		configs[i].Host = strings.ToLower(strings.TrimSpace(configs[i].Host))
		old := byHost[configs[i].Host]
		if configs[i].Username == "" {
			configs[i].Password = ""
		} else if configs[i].Password == "" {
			configs[i].Password = old.Password
		}
		if configs[i].ProxyURL == "" {
			configs[i].ProxyURL = old.ProxyURL
		}
		if configs[i].CACertPEM == "" {
			configs[i].CACertPEM = old.CACertPEM
		}
	}
	b, err := json.Marshal(configs)
	if err != nil {
		return err
	}
	return s.SetSecretSetting(ctx, registriesSettingsKey, string(b))
}
