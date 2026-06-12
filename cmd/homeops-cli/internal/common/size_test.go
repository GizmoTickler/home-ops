package common

import (
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
