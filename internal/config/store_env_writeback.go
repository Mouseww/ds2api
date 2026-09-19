package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func envWritebackEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DS2API_ENV_WRITEBACK")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func (s *Store) IsEnvWritebackEnabled() bool {
	return envWritebackEnabled()
}

func (s *Store) HasEnvConfigSource() bool {
	rawCfg := strings.TrimSpace(os.Getenv("DS2API_CONFIG_JSON"))
	return rawCfg != ""
}

func (s *Store) ConfigPath() string {
	return s.path
}

func writeConfigFile(path string, cfg Config) error {
	persistCfg := cfg.Clone()
	persistCfg.ClearAccountTokens()
	b, err := json.MarshalIndent(persistCfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigBytes(path, b)
}

func writeConfigBytes(path string, b []byte) error {
	if st, statErr := os.Stat(path); statErr == nil && st.IsDir() {
		return fmt.Errorf("save config: %s", configPathDirectoryHint(path))
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return os.WriteFile(path, b, 0o644)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}
	return os.WriteFile(path, b, 0o644)
}

// configPathDirectoryHint explains why a config path can be a directory and how
// to repair it. Docker creates an empty directory when a single-file bind mount
// source (for example ./config.json) does not exist on the host, which
// previously surfaced as a bare "is a directory" error and crash-looped the
// container.
func configPathDirectoryHint(path string) string {
	return fmt.Sprintf("config path %s is a directory, not a file: Docker creates an empty directory when a single-file bind mount source is missing on the host; remove that directory and create the file on the host (`rm -rf config.json && cp config.example.json config.json`), then restart", path)
}
