package zpt

import (
	"os"
	"strconv"
	"strings"
)

const ipLocalPortRangeFile = "/proc/sys/net/ipv4/ip_local_port_range"

// PortRangeOverlaps reports whether [base, base+count) intersects the
// inclusive range [lo, hi].
func PortRangeOverlaps(base, count, lo, hi int) bool {
	if count <= 0 {
		return false
	}
	return base <= hi && base+count-1 >= lo
}

// ParsePortRange parses the content of ip_local_port_range ("32768\t60999").
func ParsePortRange(content string) (lo, hi int, ok bool) {
	f := strings.Fields(content)
	if len(f) != 2 {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(f[0])
	hi, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}

// EphemeralRangeOverlap reads the Linux ephemeral port range; ok is false when
// it cannot be read, in which case callers should stay silent.
func EphemeralRangeOverlap(base, count int) (overlap bool, lo, hi int, ok bool) {
	b, err := os.ReadFile(ipLocalPortRangeFile)
	if err != nil {
		return false, 0, 0, false
	}
	lo, hi, ok = ParsePortRange(string(b))
	if !ok {
		return false, 0, 0, false
	}
	return PortRangeOverlaps(base, count, lo, hi), lo, hi, true
}
