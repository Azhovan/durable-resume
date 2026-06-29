package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRate_OK(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want int64
	}{
		{name: "empty unlimited", in: "", want: 0},
		{name: "whitespace unlimited", in: "   ", want: 0},
		{name: "zero", in: "0", want: 0},
		{name: "zero k", in: "0k", want: 0},
		{name: "bare bytes", in: "100000", want: 100000},
		{name: "1024 bytes", in: "1024", want: 1024},
		{name: "explicit B", in: "1024B", want: 1024},
		{name: "lower b", in: "1024b", want: 1024},
		{name: "500k", in: "500k", want: 512000},
		{name: "500K", in: "500K", want: 512000},
		{name: "1M", in: "1M", want: 1048576},
		{name: "1m", in: "1m", want: 1048576},
		{name: "1.5M", in: "1.5M", want: 1572864},
		{name: "1MiB", in: "1MiB", want: 1048576},
		{name: "1mib", in: "1mib", want: 1048576},
		{name: "1KiB", in: "1KiB", want: 1024},
		{name: "1kib", in: "1kib", want: 1024},
		{name: "1G", in: "1G", want: 1073741824},
		{name: "1GiB", in: "1GiB", want: 1073741824},
		{name: "trim surrounding space", in: "  500k  ", want: 512000},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseRate(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseRate_Err(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "negative", in: "-5"},
		{name: "garbage", in: "abc"},
		{name: "unknown unit", in: "1X"},
		{name: "unit no number", in: "k"},
		{name: "two dots", in: "1.2.3"},
		{name: "internal space", in: "1 M"},
		{name: "leading unit", in: "M1"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseRate(tt.in)
			require.Error(t, err)
		})
	}
}
