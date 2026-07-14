package common

import "math"

// Bounds-checked numeric conversions.
//
// These guard the narrow set of conversions where a value that originates
// outside this process (a hypervisor API response, an archive header field, a
// user-supplied count) is narrowed to a smaller integer type. Each helper
// returns the converted value plus an ok flag; when the input does not fit the
// destination type the value is clamped to the nearest representable bound and
// ok is false so callers can log/react. The fuzz corpus for ParseSizeSpec has
// already demonstrated that this overflow class is reachable in practice, so
// these conversions are guarded rather than blindly trusted.

// SafeUint64ToInt narrows a uint64 to a platform int. On 32-bit platforms this
// also guards the reduced int range. Overflow clamps to math.MaxInt.
func SafeUint64ToInt(v uint64) (int, bool) {
	if v > uint64(math.MaxInt) {
		return math.MaxInt, false
	}
	return int(v), true
}

// SafeIntToInt32 narrows an int to an int32, clamping to the int32 bounds.
func SafeIntToInt32(v int) (int32, bool) {
	if v > math.MaxInt32 {
		return math.MaxInt32, false
	}
	if v < math.MinInt32 {
		return math.MinInt32, false
	}
	return int32(v), true
}

// SafeInt64ToUint32 narrows an int64 to a uint32. Negative values clamp to 0
// and values above math.MaxUint32 clamp to math.MaxUint32.
func SafeInt64ToUint32(v int64) (uint32, bool) {
	if v < 0 {
		return 0, false
	}
	if v > math.MaxUint32 {
		return math.MaxUint32, false
	}
	return uint32(v), true
}
