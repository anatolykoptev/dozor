package main

import (
	"bufio"
	"os"
	"strings"
)

// loadDotenv loads a .env file into os environment if it exists.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Don't override existing env vars
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
