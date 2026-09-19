package config

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateBootstrapTestEnv clears ambient config sources so a test only observes
// the config path it sets itself.
func isolateBootstrapTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DS2API_CONFIG_JSON", "")
	t.Setenv("DS2API_CONFIG_PATH", "")
	t.Setenv("DS2API_ENV_WRITEBACK", "")
	t.Setenv("VERCEL", "")
	t.Setenv("NOW_REGION", "")
}

// captureBootstrapLogger redirects the package logger into a buffer so a test
// can assert on the warning an operator would actually see.
func captureBootstrapLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := Logger
	Logger = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { Logger = previous })
	return buf
}

func requireEmptyFileBackedStore(t *testing.T, store *Store, err error, context string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: LoadStoreWithError() error = %v, want nil", context, err)
	}
	if store == nil {
		t.Fatalf("%s: LoadStoreWithError() returned nil store", context)
	}
	if got := len(store.Keys()); got != 0 {
		t.Fatalf("%s: bootstrapped store has %d keys, want 0", context, got)
	}
	if got := len(store.Accounts()); got != 0 {
		t.Fatalf("%s: bootstrapped store has %d accounts, want 0", context, got)
	}
	if store.IsEnvBacked() {
		t.Fatalf("%s: bootstrapped store must stay file-backed (fromEnv=false)", context)
	}
}

// A bare `docker run` resolves /data/config.json without setting
// DS2API_CONFIG_PATH, so a missing file there must not stop the process: under
// `restart: always` that exit turns into an endless crash loop.
func TestLoadStoreWithErrorBootstrapsMissingDefaultConfigFile(t *testing.T) {
	isolateBootstrapTestEnv(t)
	workDir := t.TempDir()
	t.Chdir(workDir)

	store, err := LoadStoreWithError()
	requireEmptyFileBackedStore(t, store, err, "missing default config file")

	if _, statErr := os.Stat(store.ConfigPath()); !os.IsNotExist(statErr) {
		t.Fatalf("config path %q must not exist before the first save, stat error = %v", store.ConfigPath(), statErr)
	}
	wantDir, wantErr := os.Stat(workDir)
	if wantErr != nil {
		t.Fatalf("stat work dir: %v", wantErr)
	}
	gotDir, gotErr := os.Stat(filepath.Dir(store.ConfigPath()))
	if gotErr != nil {
		t.Fatalf("stat config dir: %v", gotErr)
	}
	if !os.SameFile(wantDir, gotDir) {
		t.Fatalf("config path %q is not inside the isolated work dir %q", store.ConfigPath(), workDir)
	}
}

// Docker creates an empty directory when a single-file bind mount source is
// missing on the host, so the resolved config path can be a directory. That must
// bootstrap with a warning telling the operator how to repair it.
func TestLoadStoreWithErrorBootstrapsConfigPathDirectory(t *testing.T) {
	isolateBootstrapTestEnv(t)
	dirPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatalf("create directory at config path: %v", err)
	}
	t.Setenv("DS2API_CONFIG_PATH", dirPath)

	logs := captureBootstrapLogger(t)

	store, err := LoadStoreWithError()
	requireEmptyFileBackedStore(t, store, err, "config path is a directory")

	logged := logs.String()
	for _, want := range []string{
		"level=WARN",
		"reason=",
		"is a directory",
		"config.example.json",
		"empty file-backed config",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("bootstrap warning missing %q, got: %s", want, logged)
		}
	}
}

func TestConfigBootstrapReasonCoversUnavailableConfigs(t *testing.T) {
	dirPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatalf("create directory at config path: %v", err)
	}

	// Reading a directory never returns os.ErrNotExist, so the path-type check
	// has to classify it as bootstrap-able on every platform.
	reason, ok := configBootstrapReason(errors.New("read config: is a directory"), dirPath)
	if !ok {
		t.Fatal("configBootstrapReason() did not bootstrap a directory config path")
	}
	if !strings.Contains(reason, "config.example.json") {
		t.Fatalf("directory reason lacks actionable guidance: %q", reason)
	}

	missing := filepath.Join(t.TempDir(), "config.json")
	_, missingErr := os.ReadFile(missing)
	if missingErr == nil {
		t.Fatal("expected reading a missing config file to fail")
	}
	reason, ok = configBootstrapReason(missingErr, missing)
	if !ok {
		t.Fatalf("configBootstrapReason() did not bootstrap missing file error %v", missingErr)
	}
	if !strings.Contains(reason, "does not exist") {
		t.Fatalf("missing-file reason lacks guidance: %q", reason)
	}

	if _, ok := configBootstrapReason(errors.New("invalid character 'x' looking for beginning of value"), missing); ok {
		t.Fatal("configBootstrapReason() must not bootstrap content errors")
	}
}

func TestLoadStoreWithErrorKeepsInvalidConfigContentFatal(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		isolateBootstrapTestEnv(t)
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
			t.Fatalf("write invalid config: %v", err)
		}
		t.Setenv("DS2API_CONFIG_PATH", path)

		if _, err := LoadStoreWithError(); err == nil {
			t.Fatal("LoadStoreWithError() error = nil, want error for invalid JSON config")
		}
	})

	t.Run("semantic validation", func(t *testing.T) {
		isolateBootstrapTestEnv(t)
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"admin":{"jwt_expire_hours":99999}}`), 0o644); err != nil {
			t.Fatalf("write invalid config: %v", err)
		}
		t.Setenv("DS2API_CONFIG_PATH", path)

		store, err := LoadStoreWithError()
		if err == nil {
			t.Fatal("LoadStoreWithError() error = nil, want error for semantically invalid config")
		}
		if !strings.Contains(err.Error(), "admin.jwt_expire_hours") {
			t.Fatalf("expected admin.jwt_expire_hours validation error, got %v", err)
		}
		if store != nil {
			t.Fatal("expected nil store when config content is invalid")
		}
	})
}
