package registry

import (
	"fmt"
	"regexp"
	"strconv"
)

var strictSemverTag = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)$`)

type semanticVersion struct{ major, minor, patch int }

func parseSemanticVersion(tag string) (semanticVersion, bool) {
	m := strictSemverTag.FindStringSubmatch(tag)
	if m == nil {
		return semanticVersion{}, false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return semanticVersion{major, minor, patch}, true
}

func IsStrictSemverTag(tag string) bool { return strictSemverTag.MatchString(tag) }

func (v semanticVersion) greater(other semanticVersion) bool {
	if v.major != other.major {
		return v.major > other.major
	}
	if v.minor != other.minor {
		return v.minor > other.minor
	}
	return v.patch > other.patch
}

// SelectSemverCandidates returns the highest compatible tag for each scope.
// Only strict stable x.y.z/vx.y.z tags participate: prereleases and distro
// variants are never crossed implicitly.
func SelectSemverCandidates(tags map[string]string, currentTag string) map[string]string {
	current, ok := parseSemanticVersion(currentTag)
	if !ok {
		return nil
	}
	type choice struct {
		tag     string
		version semanticVersion
	}
	choices := map[string]choice{}
	for tag := range tags {
		candidate, valid := parseSemanticVersion(tag)
		if !valid || !candidate.greater(current) {
			continue
		}
		for _, scope := range []string{"major", "minor", "patch"} {
			compatible := scope == "major" || (candidate.major == current.major && scope == "minor") ||
				(candidate.major == current.major && candidate.minor == current.minor && scope == "patch")
			if compatible && (choices[scope].tag == "" || candidate.greater(choices[scope].version)) {
				choices[scope] = choice{tag, candidate}
			}
		}
	}
	out := map[string]string{}
	for scope, choice := range choices {
		out[scope] = choice.tag
	}
	return out
}

func ReplaceTag(ref Ref, tag string) string {
	prefix := ref.Repository
	if ref.Registry != "registry-1.docker.io" {
		prefix = ref.Registry + "/" + prefix
	} else if len(ref.Repository) > len("library/") && ref.Repository[:len("library/")] == "library/" {
		prefix = ref.Repository[len("library/"):]
	}
	return fmt.Sprintf("%s:%s", prefix, tag)
}
