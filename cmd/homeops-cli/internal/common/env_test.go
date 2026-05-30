package common

import "testing"

func TestEnvBool(t *testing.T) {
	const key = "HOMEOPS_ENVBOOL_TEST"
	cases := []struct {
		val  string
		set  bool
		def  bool
		want bool
	}{
		{set: false, def: true, want: true},   // unset -> default
		{set: false, def: false, want: false}, // unset -> default
		{val: "false", set: true, def: true, want: false},
		{val: "0", set: true, def: true, want: false},
		{val: "true", set: true, def: false, want: true},
		{val: "1", set: true, def: false, want: true},
		{val: " false ", set: true, def: true, want: false}, // trimmed
		{val: "garbage", set: true, def: true, want: true},  // unparseable -> default
	}
	for _, c := range cases {
		t.Setenv(key, "")
		if c.set {
			t.Setenv(key, c.val)
		}
		if got := EnvBool(key, c.def); got != c.want {
			t.Errorf("EnvBool(%q=%q set=%v def=%v) = %v, want %v", key, c.val, c.set, c.def, got, c.want)
		}
	}
}
