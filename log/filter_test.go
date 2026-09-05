package log

import (
	"testing"
)

func TestNoiseFilter_DefaultPatterns(t *testing.T) {
	filter := NewNoiseFilter(LevelInfo)

	for _, pattern := range defaultNoisePatterns {
		// Should mute at LevelInfo
		if !filter.ShouldMute(LevelInfo, "", "error occurred: "+pattern) {
			t.Errorf("expected noise pattern %q to be muted at LevelInfo", pattern)
		}
		// Should mute at LevelDebug
		if !filter.ShouldMute(LevelDebug, "", pattern) {
			t.Errorf("expected noise pattern %q to be muted at LevelDebug", pattern)
		}
		// Should NOT mute at LevelWarn (level < minLevel)
		if filter.ShouldMute(LevelWarn, "", pattern) {
			t.Errorf("expected noise pattern %q NOT to be muted at LevelWarn", pattern)
		}
		// Should NOT mute at LevelError
		if filter.ShouldMute(LevelError, "", pattern) {
			t.Errorf("expected noise pattern %q NOT to be muted at LevelError", pattern)
		}
	}

	// Normal messages should never be muted
	if filter.ShouldMute(LevelInfo, "", "outbound connection created") {
		t.Errorf("normal message should not be muted")
	}
}

func TestNoiseFilter_CustomPatterns(t *testing.T) {
	filter := NewNoiseFilter(LevelInfo, "custom network issue", "another noise")

	if !filter.ShouldMute(LevelInfo, "", "custom network issue encountered") {
		t.Errorf("expected custom pattern to be muted")
	}
	if !filter.ShouldMute(LevelInfo, "", "another noise here") {
		t.Errorf("expected custom pattern to be muted")
	}
	if filter.ShouldMute(LevelInfo, "", "other log message") {
		t.Errorf("expected unrelated message not to be muted")
	}
}

func TestFilterFunc(t *testing.T) {
	called := false
	var fn Filter = FilterFunc(func(level Level, tag string, message string) bool {
		called = true
		return message == "mute me"
	})

	if !fn.ShouldMute(LevelInfo, "", "mute me") {
		t.Errorf("expected FilterFunc to return true")
	}
	if !called {
		t.Errorf("expected FilterFunc to be called")
	}
	if fn.ShouldMute(LevelInfo, "", "allow me") {
		t.Errorf("expected FilterFunc to return false")
	}
}
