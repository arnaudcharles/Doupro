package main

import "testing"

// TestValidateDockerSocketConfigBlocksPlainTCPByDefault guards the fix for
// the security gap a security review of issue #10's tcp:// support found:
// a plain tcp:// Docker socket is equivalent to unauthenticated remote root
// on the target host for anyone who can reach that port, so the daemon
// must refuse to start with one unless the operator explicitly opts in via
// DOUPRO_ALLOW_INSECURE_DOCKER_TCP (see .env.example and SECURITY.md).
func TestValidateDockerSocketConfigBlocksPlainTCPByDefault(t *testing.T) {
	cases := []struct {
		name             string
		socketPath       string
		allowInsecureTCP bool
		wantErr          bool
	}{
		{"unix socket path is always fine", "/var/run/docker.sock", false, false},
		{"unix:// URL is always fine", "unix:///var/run/docker.sock", false, false},
		{"tcp:// without opt-in is refused", "tcp://172.17.0.1:2375", false, true},
		{"tcp:// with opt-in is allowed", "tcp://172.17.0.1:2375", true, false},
		{"tcp:// opt-in doesn't affect a unix path", "/var/run/docker.sock", true, false},
		// Regression cases: an uppercase scheme or stray whitespace (e.g. a
		// copy-pasted .env line) must not bypass the gate — see
		// docker.IsTCPSocket, the single case/whitespace-insensitive check
		// this function and internal/docker.New both call.
		{"uppercase TCP:// without opt-in is refused", "TCP://172.17.0.1:2375", false, true},
		{"leading whitespace tcp:// without opt-in is refused", "  tcp://172.17.0.1:2375", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDockerSocketConfig(tc.socketPath, tc.allowInsecureTCP)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateDockerSocketConfig(%q, %v) = %v, want error: %v", tc.socketPath, tc.allowInsecureTCP, err, tc.wantErr)
			}
		})
	}
}
