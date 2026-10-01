// Package dotenv reads the local development .env (KEY=VALUE lines) that `make env` generates.
package dotenv

import (
	"bufio"
	"os"
	"strings"
)

// Load reads KEY=VALUE lines from path. A missing file yields an empty map.
func Load(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out, sc.Err()
}

// Get prefers the process environment, then the map.
func Get(env map[string]string, key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return env[key]
}

// GetOr is Get with a default for an unset key.
func GetOr(env map[string]string, key, def string) string {
	if v := Get(env, key); v != "" {
		return v
	}
	return def
}
