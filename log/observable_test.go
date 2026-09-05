package log

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestObservableLogger_WithFilter(t *testing.T) {
	var buf bytes.Buffer
	factory := NewDefaultFactory(
		context.Background(),
		Formatter{BaseTime: time.Now()},
		&buf,
		"",
		nil,
		true,
	)
	factory.SetFilter(NewNoiseFilter(LevelInfo))

	if err := factory.Start(); err != nil {
		t.Fatalf("factory.Start failed: %v", err)
	}
	defer factory.Close()

	sub, _, err := factory.Subscribe()
	if err != nil {
		t.Fatalf("factory.Subscribe failed: %v", err)
	}
	defer factory.UnSubscribe(sub)

	logger := factory.Logger()

	// 1. Info level with noisy log -> should NOT write to buf, but SHOULD emit to subscriber
	logger.Info("connection timed out")
	if buf.Len() > 0 {
		t.Errorf("expected buffer to be empty for noisy info log, got: %s", buf.String())
	}
	select {
	case entry := <-sub:
		if entry.Level != LevelInfo {
			t.Errorf("expected subscriber level %v, got %v", LevelInfo, entry.Level)
		}
	case <-time.After(200 * time.Millisecond):
		t.Errorf("expected subscriber to receive noisy info log")
	}

	// 2. Info level with normal log -> should write to both buf and subscriber
	buf.Reset()
	logger.Info("normal info log")
	if buf.Len() == 0 {
		t.Errorf("expected buffer to have output for normal info log")
	}
	select {
	case entry := <-sub:
		if entry.Level != LevelInfo {
			t.Errorf("expected subscriber level %v, got %v", LevelInfo, entry.Level)
		}
	case <-time.After(200 * time.Millisecond):
		t.Errorf("expected subscriber to receive normal info log")
	}

	// 3. Warn level with noisy pattern -> should NOT be muted (level < LevelInfo)
	buf.Reset()
	logger.Warn("connection timed out")
	if buf.Len() == 0 {
		t.Errorf("expected buffer to have output for warn log even if it contains noise pattern")
	}
}

func TestObservableLogger_WithoutFilter(t *testing.T) {
	var buf bytes.Buffer
	factory := NewDefaultFactory(
		context.Background(),
		Formatter{BaseTime: time.Now()},
		&buf,
		"",
		nil,
		false,
	)
	// No filter attached

	if err := factory.Start(); err != nil {
		t.Fatalf("factory.Start failed: %v", err)
	}
	defer factory.Close()

	logger := factory.Logger()

	// All logs should be written to buf
	logger.Info("connection timed out")
	if buf.Len() == 0 {
		t.Errorf("expected buffer to have output when filter is nil")
	}
}
