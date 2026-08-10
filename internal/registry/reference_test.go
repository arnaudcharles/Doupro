package registry

import "testing"

func TestIsDigestReference(t *testing.T) {
	const (
		sha256Digest = "sha256:247f7abff9f7097bbdab57df76fedd124d1e24a6ec4944fb5ef0ad128997ce05"
		sha512Digest = "sha512:247f7abff9f7097bbdab57df76fedd124d1e24a6ec4944fb5ef0ad128997ce05247f7abff9f7097bbdab57df76fedd124d1e24a6ec4944fb5ef0ad128997ce05"
	)
	tests := []struct {
		image string
		want  bool
	}{
		{image: sha256Digest, want: true},
		{image: "getgrav/grav@" + sha256Digest, want: true},
		{image: "registry.example/team/image@" + sha512Digest, want: true},
		{image: "sha256:not-a-valid-digest", want: false},
		{image: "getgrav/grav:latest", want: false},
		{image: "registry.example:5000/team/image:release", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			if got := IsDigestReference(tt.image); got != tt.want {
				t.Fatalf("IsDigestReference(%q) = %v, want %v", tt.image, got, tt.want)
			}
		})
	}
}
