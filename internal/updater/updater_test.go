package updater

import "testing"

func TestStableImageReference(t *testing.T) {
	const (
		rawDigest       = "sha256:247f7abff9f7097bbdab57df76fedd124d1e24a6ec4944fb5ef0ad128997ce05"
		canonicalDigest = "nginx@sha256:c7a6ad68be85142c7fe1089e48faa1e7c7166a194caa9180ddea66345876b9d2"
	)
	tests := []struct {
		name      string
		operation string
		stored    string
		want      string
	}{
		{name: "normal update keeps target tag", operation: "nginx:latest", stored: "nginx:latest", want: "nginx:latest"},
		{name: "scheduled digest preserves defined tag", operation: canonicalDigest, stored: "nginx:latest", want: "nginx:latest"},
		{name: "failed update after rollback preserves defined tag", operation: rawDigest, stored: "nginx:latest", want: "nginx:latest"},
		{name: "digest-only container remains digest-only", operation: rawDigest, stored: rawDigest, want: rawDigest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stableImageReference(tt.operation, tt.stored); got != tt.want {
				t.Fatalf("stableImageReference(%q, %q) = %q, want %q", tt.operation, tt.stored, got, tt.want)
			}
		})
	}
}
