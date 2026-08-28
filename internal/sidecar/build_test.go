package sidecar

import (
	"context"
	"testing"

	"github.com/Anastylosis/Custodian/internal/config"
)

func baseCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("/this/file/does/not/exist")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func TestBuild_NilConfig(t *testing.T) {
	mv, cleanup, err := Build(context.Background(), nil)
	if mv != nil || err != nil {
		t.Fatalf("Build(nil) = (%v, %v), want (nil, nil)", mv, err)
	}
	cleanup() // must not panic
}

func TestBuild_Disabled(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Sidecars.Enabled = false
	mv, cleanup, err := Build(context.Background(), cfg)
	if mv != nil || err != nil {
		t.Fatalf("Build(disabled) = (%v, %v), want (nil, nil)", mv, err)
	}
	cleanup()
}

func TestBuild_AccessOff(t *testing.T) {
	for _, access := range []string{"off", "none", " OFF "} {
		t.Run(access, func(t *testing.T) {
			cfg := baseCfg(t)
			cfg.Sidecars.Enabled = true
			cfg.Sidecars.Access = access
			mv, cleanup, err := Build(context.Background(), cfg)
			if mv != nil || err != nil {
				t.Fatalf("Build(access=%q) = (%v, %v), want (nil, nil)", access, mv, err)
			}
			cleanup()
		})
	}
}

func TestBuild_LocalMode(t *testing.T) {
	for _, access := range []string{"local", "", "auto", " Local "} {
		t.Run(access, func(t *testing.T) {
			cfg := baseCfg(t)
			cfg.Sidecars.Enabled = true
			cfg.Sidecars.Access = access
			mv, cleanup, err := Build(context.Background(), cfg)
			defer cleanup()
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if mv == nil {
				t.Fatal("expected a non-nil Mover for local mode")
			}
			if mv.Describe() != "local filesystem" && mv.Describe() == "" {
				t.Errorf("unexpected Describe(): %q", mv.Describe())
			}
		})
	}
}

func TestBuild_AutoPicksSSHWhenHostSet(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Sidecars.Enabled = true
	cfg.Sidecars.Access = "auto"
	cfg.Sidecars.SSH.Host = "127.0.0.1"
	cfg.Sidecars.SSH.User = "nobody"
	cfg.Sidecars.SSH.Port = 1 // nothing listens here; dial fails fast

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // avoid depending on real network timing/availability

	mv, cleanup, err := Build(ctx, cfg)
	defer cleanup()
	if err == nil {
		t.Fatal("expected an error connecting over ssh")
	}
	if mv != nil {
		t.Errorf("expected nil Mover on ssh failure, got %v", mv)
	}
}

func TestBuild_UnknownAccessMode(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Sidecars.Enabled = true
	cfg.Sidecars.Access = "carrier-pigeon"
	mv, cleanup, err := Build(context.Background(), cfg)
	defer cleanup()
	if err == nil {
		t.Fatal("expected an error for an unknown access mode")
	}
	if mv != nil {
		t.Errorf("expected nil Mover, got %v", mv)
	}
}

func TestBuild_BadPathMapRule(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Sidecars.Enabled = true
	cfg.Sidecars.Access = "local"
	cfg.Sidecars.PathMap = []config.PathMapRule{{Stash: "", Local: "/mnt/lib"}}
	mv, cleanup, err := Build(context.Background(), cfg)
	defer cleanup()
	if err == nil {
		t.Fatal("expected an error for an invalid path_map rule")
	}
	if mv != nil {
		t.Errorf("expected nil Mover, got %v", mv)
	}
}

func TestEnvOrEmpty(t *testing.T) {
	if got := envOrEmpty(""); got != "" {
		t.Errorf("envOrEmpty(\"\") = %q, want \"\"", got)
	}
	t.Setenv("CUSTODIAN_TEST_ENVOREMPTY", "secret")
	if got := envOrEmpty("CUSTODIAN_TEST_ENVOREMPTY"); got != "secret" {
		t.Errorf("envOrEmpty = %q, want %q", got, "secret")
	}
	if got := envOrEmpty("CUSTODIAN_TEST_ENVOREMPTY_UNSET"); got != "" {
		t.Errorf("envOrEmpty(unset) = %q, want \"\"", got)
	}
}
