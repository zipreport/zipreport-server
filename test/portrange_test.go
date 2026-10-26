package test

import (
	"testing"
	"zipreport-server/pkg/zpt"

	"github.com/stretchr/testify/assert"
)

func TestPortRangeOverlaps(t *testing.T) {
	cases := []struct {
		name        string
		base, count int
		lo, hi      int
		want        bool
	}{
		{"below", 22000, 8, 32768, 60999, false},
		{"inside", 42000, 8, 32768, 60999, true},
		{"straddle low", 32765, 8, 32768, 60999, true},
		{"last port just below", 32760, 8, 32768, 60999, false},
		{"last port equals lo", 32761, 8, 32768, 60999, true},
		{"first port equals hi", 60999, 8, 32768, 60999, true},
		{"first port above hi", 61000, 8, 32768, 60999, false},
		{"empty", 42000, 0, 32768, 60999, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, zpt.PortRangeOverlaps(c.base, c.count, c.lo, c.hi))
		})
	}
}

func TestParsePortRange(t *testing.T) {
	lo, hi, ok := zpt.ParsePortRange("32768\t60999\n")
	assert.True(t, ok)
	assert.Equal(t, 32768, lo)
	assert.Equal(t, 60999, hi)
	for _, bad := range []string{"", "abc def", "1", "5 1", "1 2 3"} {
		_, _, ok = zpt.ParsePortRange(bad)
		assert.False(t, ok, bad)
	}
}
