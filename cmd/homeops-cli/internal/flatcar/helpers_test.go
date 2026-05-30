package flatcar

import "testing"

func TestKubernetesMinor(t *testing.T) {
	cases := map[string]string{
		"v1.36.1": "v1.36",
		"v1.36":   "v1.36",
		"1.34.0":  "1.34",
		"garbage": "garbage",
	}
	for in, want := range cases {
		if got := KubernetesMinor(in); got != want {
			t.Fatalf("KubernetesMinor(%q) = %q, want %q", in, got, want)
		}
	}
}
