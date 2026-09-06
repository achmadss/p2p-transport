package config

import (
	"os"
	"strconv"
	"strings"
	"time"
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

// String reads a variable, falling back when it is unset or blank.
func String(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
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
