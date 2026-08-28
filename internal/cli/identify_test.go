package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/spf13/cobra"
)

func newIdentifyTestCmd() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetIn(nil)
	return cmd, &out
}

// resetIdentifyFlags clears the package-level identify flag vars so tests
// don't leak state between runs regardless of execution order.
func resetIdentifyFlags() {
	flagIdentifyVia, flagIdentifyEndpoint, flagIdentifyLedger = "", "", ""
	flagIdentifyLimit = 0
	flagIdentifyCommit, flagIdentifyYes = false, false
}

func TestRunIdentifyRequiresVia(t *testing.T) {
	resetIdentifyFlags()
	root := NewRootCmd("test", "none", "never")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"identify"})
	if err := root.Execute(); err == nil {
		t.Fatal("identify with no --via should fail")
	}
}

func TestRunIdentifyRejectsAnUnknownVia(t *testing.T) {
	resetIdentifyFlags()
	root := NewRootCmd("test", "none", "never")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"identify", "--via", "bogus"})
	if err := root.Execute(); err == nil {
		t.Fatal("identify --via bogus should fail")
	}
}

// --- resolveIdentifyEndpoint: --endpoint, then config, then the server's
// only configured stash-box, mirroring canon.go's stashBoxSource ---

func TestResolveIdentifyEndpointFlagWins(t *testing.T) {
	resetIdentifyFlags()
	flagIdentifyEndpoint = "https://flag.test/graphql"
	cfg := &config.Config{}
	cfg.Sources.StashBoxEndpoint = "https://config.test/graphql"

	got, err := resolveIdentifyEndpoint(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("resolveIdentifyEndpoint: %v", err)
	}
	if got != "https://flag.test/graphql" {
		t.Errorf("got %q, want the flag's endpoint", got)
	}
}

func TestResolveIdentifyEndpointFallsBackToConfig(t *testing.T) {
	resetIdentifyFlags()
	cfg := &config.Config{}
	cfg.Sources.StashBoxEndpoint = "https://config.test/graphql"

	got, err := resolveIdentifyEndpoint(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("resolveIdentifyEndpoint: %v", err)
	}
	if got != "https://config.test/graphql" {
		t.Errorf("got %q, want the configured endpoint", got)
	}
}

func TestResolveIdentifyEndpointDefaultsToTheOnlyConfiguredStashBox(t *testing.T) {
	resetIdentifyFlags()
	client := stashBoxesServer(t, "https://only.test/graphql")

	got, err := resolveIdentifyEndpoint(context.Background(), &config.Config{}, client)
	if err != nil {
		t.Fatalf("resolveIdentifyEndpoint: %v", err)
	}
	if got != "https://only.test/graphql" {
		t.Errorf("got %q, want the server's only stash-box", got)
	}
}

func TestResolveIdentifyEndpointRequiresOneWhenServerHasSeveral(t *testing.T) {
	resetIdentifyFlags()
	client := stashBoxesServer(t, "https://a.test/graphql", "https://b.test/graphql")

	if _, err := resolveIdentifyEndpoint(context.Background(), &config.Config{}, client); err == nil {
		t.Fatal("resolveIdentifyEndpoint should fail when no endpoint is given and the server has more than one stash-box")
	}
}

// --- confirmIdentify: reads the ledger only, shows count + endpoint ---

func TestConfirmIdentifyURLShowsCountNoEndpoint(t *testing.T) {
	resetIdentifyFlags()
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 100, 1920, 1080, 1000, "hevc")
	if _, err := l.DB().ExecContext(ctx, `INSERT INTO scene_url(scene_id, url) VALUES('1', 'https://example/1')`); err != nil {
		t.Fatalf("inserting scene_url: %v", err)
	}

	flagIdentifyYes = true
	cmd, out := newIdentifyTestCmd()
	ok, err := confirmIdentify(ctx, cmd, l, "url", "", 0)
	if err != nil {
		t.Fatalf("confirmIdentify: %v", err)
	}
	if !ok {
		t.Fatal("confirmIdentify should proceed with --yes")
	}
	if !strings.Contains(out.String(), "considering: 1 scene(s) via url") {
		t.Errorf("output missing count: %q", out.String())
	}
	if strings.Contains(out.String(), "endpoint:") {
		t.Errorf("url mode should not print an endpoint line: %q", out.String())
	}
}

func TestConfirmIdentifyStashBoxShowsCountAndEndpoint(t *testing.T) {
	resetIdentifyFlags()
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 100, 1920, 1080, 1000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 100, 1920, 1080, 1000, "hevc")

	flagIdentifyYes = true
	cmd, out := newIdentifyTestCmd()
	ok, err := confirmIdentify(ctx, cmd, l, "stash-box", "https://sb.test/graphql", 0)
	if err != nil {
		t.Fatalf("confirmIdentify: %v", err)
	}
	if !ok {
		t.Fatal("confirmIdentify should proceed with --yes")
	}
	if !strings.Contains(out.String(), "considering: 2 scene(s) via stash-box") {
		t.Errorf("output missing count: %q", out.String())
	}
	if !strings.Contains(out.String(), "endpoint: https://sb.test/graphql") {
		t.Errorf("output missing endpoint: %q", out.String())
	}
	if !strings.Contains(out.String(), "ABOUT TO IDENTIFY VIA STASH-BOX") {
		t.Errorf("output missing the YES-prompt header: %q", out.String())
	}
}

func TestConfirmIdentifyRespectsLimit(t *testing.T) {
	resetIdentifyFlags()
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 100, 1920, 1080, 1000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 100, 1920, 1080, 1000, "hevc")

	flagIdentifyYes = true
	cmd, out := newIdentifyTestCmd()
	if _, err := confirmIdentify(ctx, cmd, l, "stash-box", "", 1); err != nil {
		t.Fatalf("confirmIdentify: %v", err)
	}
	if !strings.Contains(out.String(), "considering: 1 scene(s)") {
		t.Errorf("output should reflect the limit: %q", out.String())
	}
}

func TestConfirmIdentifyPropagatesLookupError(t *testing.T) {
	resetIdentifyFlags()
	l := newPlanTestLedger(t)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	flagIdentifyYes = true
	cmd, _ := newIdentifyTestCmd()
	if _, err := confirmIdentify(context.Background(), cmd, l, "stash-box", "", 0); err == nil {
		t.Fatal("confirmIdentify should propagate a lookup error from a closed ledger")
	}
}
