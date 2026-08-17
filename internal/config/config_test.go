package config

import "testing"

// TestLoadDefaultsAllowInsecureTCPToFalse guards the fail-closed default
// for DOUPRO_ALLOW_INSECURE_DOCKER_TCP: an operator who never sets it must
// get the safe (refuse tcp://) behavior, not an accidental opt-in — see
// cmd/doupro/cmd_serve.go's validateDockerSocketConfig.
func TestLoadDefaultsAllowInsecureTCPToFalse(t *testing.T) {
	t.Setenv("DOUPRO_ALLOW_INSECURE_DOCKER_TCP", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowInsecureTCP {
		t.Fatal("AllowInsecureTCP defaulted to true with the env var unset")
	}
}

func TestLoadReadsAllowInsecureTCP(t *testing.T) {
	t.Setenv("DOUPRO_ALLOW_INSECURE_DOCKER_TCP", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowInsecureTCP {
		t.Fatal("AllowInsecureTCP did not pick up DOUPRO_ALLOW_INSECURE_DOCKER_TCP=true")
	}
}

func TestLoadDefaultsSocketPathToTheDockerSocket(t *testing.T) {
	t.Setenv("DOUPRO_DOCKER_SOCKET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SocketPath != "/var/run/docker.sock" {
		t.Fatalf("SocketPath = %q, want the documented default", cfg.SocketPath)
	}
}
