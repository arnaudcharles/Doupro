package registry

import "testing"

func TestSelectSemverCandidates(t *testing.T) {
	tags := map[string]string{"1.2.3": "a", "1.2.5": "b", "1.4.0": "c", "2.0.0": "d", "2.1.0-rc1": "e", "latest": "d"}
	got := SelectSemverCandidates(tags, "1.2.3")
	if got["patch"] != "1.2.5" || got["minor"] != "1.4.0" || got["major"] != "2.0.0" {
		t.Fatalf("got=%v", got)
	}
}

func TestSelectSemverCandidatesRejectsNonStrictCurrentTag(t *testing.T) {
	if got := SelectSemverCandidates(map[string]string{"17.1.0-alpine": "x"}, "16.2.0-alpine"); len(got) != 0 {
		t.Fatalf("got=%v", got)
	}
}
