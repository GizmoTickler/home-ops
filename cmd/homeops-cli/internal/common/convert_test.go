package common

import (
	"math"
	"testing"
)

func TestSafeUint64ToInt(t *testing.T) {
	tests := []struct {
		name   string
		in     uint64
		want   int
		wantOK bool
	}{
		{"zero", 0, 0, true},
		{"small", 42, 42, true},
		{"maxint", uint64(math.MaxInt), math.MaxInt, true},
		{"over max clamps", math.MaxUint64, math.MaxInt, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SafeUint64ToInt(tt.in)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("SafeUint64ToInt(%d) = (%d, %v), want (%d, %v)", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestSafeIntToInt32(t *testing.T) {
	tests := []struct {
		name   string
		in     int
		want   int32
		wantOK bool
	}{
		{"zero", 0, 0, true},
		{"small", 1234, 1234, true},
		{"max", math.MaxInt32, math.MaxInt32, true},
		{"min", math.MinInt32, math.MinInt32, true},
		{"over max clamps", math.MaxInt32 + 1, math.MaxInt32, false},
		{"under min clamps", math.MinInt32 - 1, math.MinInt32, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SafeIntToInt32(tt.in)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("SafeIntToInt32(%d) = (%d, %v), want (%d, %v)", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestSafeInt64ToUint32(t *testing.T) {
	tests := []struct {
		name   string
		in     int64
		want   uint32
		wantOK bool
	}{
		{"zero", 0, 0, true},
		{"small", 4321, 4321, true},
		{"max", math.MaxUint32, math.MaxUint32, true},
		{"negative clamps to zero", -1, 0, false},
		{"over max clamps", math.MaxUint32 + 1, math.MaxUint32, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SafeInt64ToUint32(tt.in)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("SafeInt64ToUint32(%d) = (%d, %v), want (%d, %v)", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
