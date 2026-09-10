package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/multiformats/go-multiaddr"
)

// Everything tunable is read from the environment with a working
// default, so a machine can be reconfigured without a rebuild. An empty
// or unparseable value falls back to the default rather than failing:
// these are knobs, not settings that change what is correct.

func Str(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func Int(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func Bytes(name string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(v, "G"), strings.HasSuffix(v, "g"):
		mult, v = 1<<30, v[:len(v)-1]
	case strings.HasSuffix(v, "M"), strings.HasSuffix(v, "m"):
		mult, v = 1<<20, v[:len(v)-1]
	case strings.HasSuffix(v, "K"), strings.HasSuffix(v, "k"):
		mult, v = 1<<10, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n * mult
}

func Duration(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(name)); err == nil && d > 0 {
		return d
	}
	return def
}

// List splits a comma-separated variable, ignoring blanks.
func List(name string) []string {
	var out []string
	for _, s := range strings.Split(os.Getenv(name), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Multiaddrs reads a comma-separated variable as addresses, failing on
// the first bad one.
//
// This is the exception to the rule above: an address the operator
// mistyped has no sensible default, and falling back to what the
// machine sees itself is exactly the case the variable exists to
// override. Better to refuse to start than to advertise an address
// that reaches nothing.
func Multiaddrs(name string) ([]multiaddr.Multiaddr, error) {
	var out []multiaddr.Multiaddr
	for _, s := range List(name) {
		ma, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			return nil, fmt.Errorf("%s: bad address %q: %w", name, s, err)
		}
		out = append(out, ma)
	}
	return out, nil
}

// Float reads a fraction or a rate. Anything unparseable falls back to
// the default, the same as the rest of the knobs above.
func Float(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return v
	}
	return def
}
