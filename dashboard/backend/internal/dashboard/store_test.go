package dashboard

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{{"20260814", "20260813", 1}, {"1.2.0", "1.1.9", 1}, {"v2.0.0", "1.99.0", 1}, {"1.0.0", "1.0.0", 0}, {"1.0.0", "1.0.1", -1}}
	for _, test := range cases {
		got := compareVersions(test.left, test.right)
		if (got > 0) != (test.want > 0) || (got < 0) != (test.want < 0) {
			t.Fatalf("compareVersions(%q, %q)=%d want sign %d", test.left, test.right, got, test.want)
		}
	}
}

func TestVersionPattern(t *testing.T) {
	for _, version := range []string{"20260814"} {
		if !versionPattern.MatchString(version) {
			t.Fatalf("version %q should be accepted", version)
		}
	}
	for _, version := range []string{"2026-08-14", "latest", "1", "0.1.0"} {
		if versionPattern.MatchString(version) {
			t.Fatalf("version %q should be rejected", version)
		}
	}
}
