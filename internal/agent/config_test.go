package agent

import "testing"

// TestLoadConfigProfileOptional guards the documented fallback: pods without a
// stoker.io/profile annotation get an empty PROFILE env var, and the agent must
// start and resolve the "default" profile instead of crash-looping at config load.
func TestLoadConfigProfileOptional(t *testing.T) {
	t.Setenv("CR_NAME", "test-cr")
	t.Setenv("POD_NAMESPACE", "test-ns")
	t.Setenv("PROFILE", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with empty PROFILE: %v", err)
	}
	if cfg.ProfileName != "" {
		t.Errorf("ProfileName = %q, want empty (lookup falls back to default)", cfg.ProfileName)
	}
}

// TestLoadConfigRequiredFields pins the two env vars the agent genuinely cannot
// run without, so the failure mode stays a clear startup error.
func TestLoadConfigRequiredFields(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "test-ns")
	t.Setenv("CR_NAME", "")
	if _, err := LoadConfig(); err == nil {
		t.Error("LoadConfig without CR_NAME should fail")
	}

	t.Setenv("CR_NAME", "test-cr")
	t.Setenv("POD_NAMESPACE", "")
	if _, err := LoadConfig(); err == nil {
		t.Error("LoadConfig without POD_NAMESPACE should fail")
	}
}
