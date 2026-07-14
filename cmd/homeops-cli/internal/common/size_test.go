package common

import (
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSizeSpec(t *testing.T) {
	cases := []struct {
		spec     string
		bytes    int64
		relative bool
		wantErr  bool
	}{
		{spec: "20G", bytes: 20 << 30},
		{spec: "+20G", bytes: 20 << 30, relative: true},
		{spec: "1T", bytes: 1 << 40},
		{spec: "512M", bytes: 512 << 20},
		{spec: "100", bytes: 100},
		{spec: "20g", bytes: 20 << 30},
		{spec: " +30G ", bytes: 30 << 30, relative: true},
		{spec: "", wantErr: true},
		{spec: "+", wantErr: true},
		{spec: "20X", wantErr: true},
		{spec: "-5G", wantErr: true},
		{spec: "0G", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			b, rel, err := ParseSizeSpec(tc.spec)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.bytes, b)
			assert.Equal(t, tc.relative, rel)
		})
	}
}

func TestParseSizeSpecAbsoluteMonotonicity(t *testing.T) {
	units := []struct {
		name string
		unit string
		max  int64
	}{
		{name: "bytes", unit: "", max: math.MaxInt64},
		{name: "megabytes", unit: "M", max: math.MaxInt64 / (1 << 20)},
		{name: "gigabytes", unit: "G", max: math.MaxInt64 / (1 << 30)},
		{name: "terabytes", unit: "T", max: math.MaxInt64 / (1 << 40)},
	}

	for _, u := range units {
		t.Run(u.name, func(t *testing.T) {
			var previous int64
			for n := int64(1); n <= 128; n++ {
				bytes, relative, err := ParseSizeSpec(strconv.FormatInt(n, 10) + u.unit)
				require.NoError(t, err)
				assert.False(t, relative, "absolute specs must not be marked relative")
				assert.GreaterOrEqual(t, bytes, previous, "absolute size specs must be monotonic")
				previous = bytes
			}

			bytes, relative, err := ParseSizeSpec(strconv.FormatInt(u.max, 10) + u.unit)
			require.NoError(t, err)
			assert.False(t, relative)
			assert.Greater(t, bytes, int64(0), "maximum non-overflowing %s spec must remain positive", u.name)

			if u.unit != "" {
				_, _, err = ParseSizeSpec(strconv.FormatInt(u.max+1, 10) + u.unit)
				require.Error(t, err, "one past the maximum %s spec must not wrap", u.name)
			}
		})
	}
}
