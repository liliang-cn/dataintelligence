package aicli

import (
	"strings"
	"testing"
)

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

// A name that is set but wrong must not fall through quietly: the command then
// produces a heuristic draft that looks like the model ran.
func TestFromEnvRejectsAnUnknownName(t *testing.T) {
	t.Setenv("DI_AGENT_CLI", "gpt")
	if _, ok := FromEnv(); ok {
		t.Error("FromEnv accepted an unknown agent")
	}
}
