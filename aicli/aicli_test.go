package aicli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// --mcp-config is variadic. Given the JSON inline it swallowed the prompt as a
// second config path and failed with a file-not-found naming the whole prompt,
// so the path is written to a file and the strict flag follows it — a flag,
// which the variadic cannot absorb.
func TestQuietArgsCannotSwallowThePrompt(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{Name: "claude"}
	args := r.quietArgs(dir)
	if len(args) == 0 {
		t.Fatal("no args")
	}
	if args[len(args)-1] != "--strict-mcp-config" {
		t.Errorf("last arg is %q; a value there would absorb the prompt", args[len(args)-1])
	}
	i := slices.Index(args, "--mcp-config")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("no --mcp-config in %v", args)
	}
	body, err := os.ReadFile(args[i+1])
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(body), `"mcpServers":{}`) {
		t.Errorf("config = %s, want no servers", body)
	}
	if filepath.Dir(args[i+1]) != dir {
		t.Errorf("config written outside the throwaway workspace: %s", args[i+1])
	}
}

// --allowed-tools "" is accepted and changes nothing, so it is not passed. A
// flag that looks like it disables something and does not is worse than none.
func TestNoFlagThatPretendsToDisableTools(t *testing.T) {
	if slices.Contains((&Runner{Name: "claude"}).quietArgs(t.TempDir()), "--allowed-tools") {
		t.Error("--allowed-tools is passed but does not restrict anything")
	}
}

// The provider already emits its own headless flags; adding them again is a
// hard error from clap, not a duplicate that gets ignored.
func TestCodexGetsNoExtraFlags(t *testing.T) {
	if args := (&Runner{Name: "codex"}).quietArgs(t.TempDir()); len(args) != 0 {
		t.Errorf("codex extra args = %v, want none (the provider adds its own)", args)
	}
}

func TestUnknownAgentIsNamed(t *testing.T) {
	if _, err := New("gpt", ""); err == nil || !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should list the options, got %v", err)
	}
}

// An unset variable means "not asked for", not "misconfigured".
func TestFromEnvSilentWhenUnset(t *testing.T) {
	t.Setenv("DI_AGENT_CLI", "")
	if _, ok := FromEnv(); ok {
		t.Error("FromEnv returned a runner with nothing configured")
	}
}
