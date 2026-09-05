package log

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
)

func TestNew_DefaultMuteNoise(t *testing.T) {
	var buf bytes.Buffer
	factory, err := New(Options{
		Context:       context.Background(),
		Options:       option.LogOptions{},
		DefaultWriter: &buf,
		BaseTime:      time.Now(),
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := factory.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer factory.Close()

	logger := factory.Logger()
	logger.Info("connection timed out")
	if buf.Len() > 0 {
		t.Errorf("expected default noisy log to be muted, got: %s", buf.String())
	}

	logger.Info("normal log")
	if buf.Len() == 0 {
		t.Errorf("expected normal log to be outputted")
	}
}

func TestNew_DisableMuteNoise(t *testing.T) {
	var buf bytes.Buffer
	factory, err := New(Options{
		Context: context.Background(),
		Options: option.LogOptions{
			MuteNoise: common.Ptr(false),
		},
		DefaultWriter: &buf,
		BaseTime:      time.Now(),
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := factory.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer factory.Close()

	logger := factory.Logger()
	logger.Info("connection timed out")
	if buf.Len() == 0 {
		t.Errorf("expected noisy log NOT to be muted when MuteNoise is false")
	}
}

func TestNew_CustomMutePatterns(t *testing.T) {
	var buf bytes.Buffer
	factory, err := New(Options{
		Context: context.Background(),
		Options: option.LogOptions{
			MutePatterns: []string{"custom bad event"},
		},
		DefaultWriter: &buf,
		BaseTime:      time.Now(),
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := factory.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer factory.Close()

	logger := factory.Logger()
	logger.Info("custom bad event occurred")
	if buf.Len() > 0 {
		t.Errorf("expected custom bad event to be muted, got: %s", buf.String())
	}
}
