package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// hasFlag checks if a flag exists in os.Args.
func hasFlag(flag string) bool {
	for _, a := range os.Args[2:] {
		if a == flag {
			return true
		}
	}
	return false
}

// getFlagValue returns the value after a flag (--flag value or --flag=value).
func getFlagValue(flag string) string {
	args := os.Args[2:]
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}

// resolveBuiltinDir finds a directory relative to the executable or cwd.
func resolveBuiltinDir(name string) string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		dir := filepath.Join(cwd, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// loadDotenv loads a .env file into os environment if it exists.
// Values in .env always take precedence over inherited OS environment.
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
		os.Setenv(key, val)
	}
}

// hashString returns a short hex hash of a string (for change detection).
func hashString(s string) string {
	h := fmt.Sprintf("%x", []byte(s))
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

// sendWebhook POSTs a JSON payload {message: ...} to the given URL.
func sendWebhook(ctx context.Context, url, text string) {
	body, _ := json.Marshal(map[string]string{"message": text})
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	if err != nil {
		slog.Error("webhook request build failed", slog.Any("error", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("webhook send failed", slog.Any("error", err))
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Error("webhook returned error", slog.Int("status", resp.StatusCode))
	}
}
