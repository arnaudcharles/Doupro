package web

import "testing"

func TestVersionDisplayUsesSourceTagInsteadOfUnresolved(t *testing.T) {
	label, exact, digest := versionDisplay(
		"tiredofit/freescout:latest",
		"sha256:07734b3730a6858009ccbc03f71bb6655747a64001c67e888ad19efd051afad9",
	)
	if label != "latest" || exact || digest != "sha256:07734b3730a6…" {
		t.Fatalf("label=%q exact=%v digest=%q", label, exact, digest)
	}
}

func TestVersionDisplayPrefersExactVersion(t *testing.T) {
	label, exact, digest := versionDisplay("tiredofit/freescout:latest", "1.17.999")
	if label != "1.17.999" || !exact || digest != "" {
		t.Fatalf("label=%q exact=%v digest=%q", label, exact, digest)
	}
}
