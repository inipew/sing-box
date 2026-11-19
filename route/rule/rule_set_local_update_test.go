package rule

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
	"github.com/stretchr/testify/require"
)

func TestLocalRuleSetManualRefreshSourceFormat(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "rules.json")
	err := os.WriteFile(filePath, []byte(`{"version":4,"rules":[{"domain":["alpha.example"]}]}`), 0o600)
	require.NoError(t, err)

	ctx := filemanager.WithDefault(context.Background(), "", tempDir, os.Getuid(), os.Getgid())
	ruleSet, err := NewLocalRuleSet(ctx, log.NewNOPFactory().Logger(), "test-local", option.RuleSet{
		Type:         C.RuleSetTypeLocal,
		Format:       C.RuleSetFormatSource,
		LocalOptions: option.LocalRuleSet{Path: filePath},
	})
	require.NoError(t, err)

	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "alpha.example"}))
	require.False(t, ruleSet.Match(&adapter.InboundContext{Domain: "beta.example"}))
	require.EqualValues(t, 1, ruleSet.ProviderInfo().RuleCount)

	var callbackCalled atomic.Bool
	ruleSet.RegisterCallback(func(adapter.RuleSet) {
		callbackCalled.Store(true)
	})

	time.Sleep(10 * time.Millisecond)
	err = os.WriteFile(filePath, []byte(`{"version":4,"rules":[{"domain":["beta.example"]},{"domain":["gamma.example"]}]}`), 0o600)
	require.NoError(t, err)

	err = ruleSet.Update(ctx)
	require.NoError(t, err)

	require.True(t, callbackCalled.Load(), "callback should be invoked on successful refresh")
	require.False(t, ruleSet.Match(&adapter.InboundContext{Domain: "alpha.example"}))
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "beta.example"}))
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "gamma.example"}))

	info := ruleSet.ProviderInfo()
	require.EqualValues(t, 2, info.RuleCount)
	stat, err := os.Stat(filePath)
	require.NoError(t, err)
	require.Equal(t, stat.ModTime(), info.UpdatedAt)
}

func TestLocalRuleSetManualRefreshBinaryFormat(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "rules.srs")

	var buf bytes.Buffer
	err := srs.Write(&buf, option.PlainRuleSet{
		Rules: []option.HeadlessRule{
			{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"bin-one.example"}}},
		},
	}, C.RuleSetVersion2)
	require.NoError(t, err)
	err = os.WriteFile(filePath, buf.Bytes(), 0o600)
	require.NoError(t, err)

	ctx := filemanager.WithDefault(context.Background(), "", tempDir, os.Getuid(), os.Getgid())
	ruleSet, err := NewLocalRuleSet(ctx, log.NewNOPFactory().Logger(), "test-bin", option.RuleSet{
		Type:         C.RuleSetTypeLocal,
		Format:       C.RuleSetFormatBinary,
		LocalOptions: option.LocalRuleSet{Path: filePath},
	})
	require.NoError(t, err)

	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "bin-one.example"}))
	require.EqualValues(t, 1, ruleSet.ProviderInfo().RuleCount)

	buf.Reset()
	err = srs.Write(&buf, option.PlainRuleSet{
		Rules: []option.HeadlessRule{
			{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"bin-two.example"}}},
			{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"bin-three.example"}}},
		},
	}, C.RuleSetVersion2)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	err = os.WriteFile(filePath, buf.Bytes(), 0o600)
	require.NoError(t, err)

	err = ruleSet.Update(ctx)
	require.NoError(t, err)

	require.False(t, ruleSet.Match(&adapter.InboundContext{Domain: "bin-one.example"}))
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "bin-two.example"}))
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "bin-three.example"}))
	require.EqualValues(t, 2, ruleSet.ProviderInfo().RuleCount)
}

func TestLocalRuleSetManualRefreshInlineIsNoOp(t *testing.T) {
	ruleSet, err := NewLocalRuleSet(context.Background(), log.NewNOPFactory().Logger(), "inline", option.RuleSet{
		Type: C.RuleSetTypeInline,
		InlineOptions: option.PlainRuleSet{
			Rules: []option.HeadlessRule{
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"inline.example"}}},
			},
		},
	})
	require.NoError(t, err)

	var callbackCalled bool
	ruleSet.RegisterCallback(func(adapter.RuleSet) {
		callbackCalled = true
	})

	err = ruleSet.Update(context.Background())
	require.NoError(t, err)
	require.False(t, callbackCalled, "callback should not be called for no-op inline update")
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "inline.example"}))
}

func TestLocalRuleSetManualRefreshMissingAndCorruptedFile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "rules.json")
	err := os.WriteFile(filePath, []byte(`{"version":4,"rules":[{"domain":["initial.example"]}]}`), 0o600)
	require.NoError(t, err)

	ctx := filemanager.WithDefault(context.Background(), "", tempDir, os.Getuid(), os.Getgid())
	ruleSet, err := NewLocalRuleSet(ctx, log.NewNOPFactory().Logger(), "test-fail", option.RuleSet{
		Type:         C.RuleSetTypeLocal,
		Format:       C.RuleSetFormatSource,
		LocalOptions: option.LocalRuleSet{Path: filePath},
	})
	require.NoError(t, err)

	initialInfo := ruleSet.ProviderInfo()
	var callbackCalled atomic.Bool
	ruleSet.RegisterCallback(func(adapter.RuleSet) {
		callbackCalled.Store(true)
	})

	// 1. Missing file
	err = os.Remove(filePath)
	require.NoError(t, err)

	err = ruleSet.Update(ctx)
	require.Error(t, err)
	require.False(t, callbackCalled.Load())
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "initial.example"}))
	require.Equal(t, initialInfo, ruleSet.ProviderInfo())

	// 2. Corrupted file
	err = os.WriteFile(filePath, []byte(`not-json`), 0o600)
	require.NoError(t, err)

	err = ruleSet.Update(ctx)
	require.Error(t, err)
	require.False(t, callbackCalled.Load())
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "initial.example"}))
	require.Equal(t, initialInfo, ruleSet.ProviderInfo())
}

func TestLocalRuleSetManualRefreshRejectsInvalidDNSMetadata(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "rules.json")
	err := os.WriteFile(filePath, []byte(`{"version":4,"rules":[{"domain":["valid.example"]}]}`), 0o600)
	require.NoError(t, err)

	ctx := filemanager.WithDefault(context.Background(), "", tempDir, os.Getuid(), os.Getgid())
	ctx = service.ContextWith[adapter.DNSRuleSetUpdateValidator](ctx, &fakeDNSRuleSetUpdateValidator{
		validate: func(tag string, metadata adapter.RuleSetMetadata) error {
			if metadata.ContainsDNSQueryTypeRule {
				return E.New("dns conflict")
			}
			return nil
		},
	})

	ruleSet, err := NewLocalRuleSet(ctx, log.NewNOPFactory().Logger(), "test-dns", option.RuleSet{
		Type:         C.RuleSetTypeLocal,
		Format:       C.RuleSetFormatSource,
		LocalOptions: option.LocalRuleSet{Path: filePath},
	})
	require.NoError(t, err)

	var callbackCalled atomic.Bool
	ruleSet.RegisterCallback(func(adapter.RuleSet) {
		callbackCalled.Store(true)
	})

	err = os.WriteFile(filePath, []byte(`{"version":4,"rules":[{"query_type":["A"]}]}`), 0o600)
	require.NoError(t, err)

	err = ruleSet.Update(ctx)
	require.ErrorContains(t, err, "dns conflict")
	require.False(t, callbackCalled.Load())
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "valid.example"}))
	require.EqualValues(t, 1, ruleSet.ProviderInfo().RuleCount)
}

func TestLocalRuleSetManualRefreshContextCancellation(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "rules.json")
	err := os.WriteFile(filePath, []byte(`{"version":4,"rules":[{"domain":["valid.example"]}]}`), 0o600)
	require.NoError(t, err)

	ctx := filemanager.WithDefault(context.Background(), "", tempDir, os.Getuid(), os.Getgid())
	ruleSet, err := NewLocalRuleSet(ctx, log.NewNOPFactory().Logger(), "test-cancel", option.RuleSet{
		Type:         C.RuleSetTypeLocal,
		Format:       C.RuleSetFormatSource,
		LocalOptions: option.LocalRuleSet{Path: filePath},
	})
	require.NoError(t, err)

	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	err = ruleSet.Update(cancelledCtx)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "valid.example"}))
}

func TestLocalRuleSetManualRefreshSerializedWithWatcher(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "rules.json")
	err := os.WriteFile(filePath, []byte(`{"version":4,"rules":[{"domain":["start.example"]}]}`), 0o600)
	require.NoError(t, err)

	ctx := filemanager.WithDefault(context.Background(), "", tempDir, os.Getuid(), os.Getgid())
	ruleSet, err := NewLocalRuleSet(ctx, log.NewNOPFactory().Logger(), "test-concur", option.RuleSet{
		Type:         C.RuleSetTypeLocal,
		Format:       C.RuleSetFormatSource,
		LocalOptions: option.LocalRuleSet{Path: filePath},
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Emulate concurrent manual updates and simulated watcher callbacks
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = ruleSet.Update(ctx)
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ruleSet.updateAccess.Lock()
				_ = ruleSet.reloadFile(ruleSet.path)
				ruleSet.updateAccess.Unlock()
			}
		}
	}()

	// Concurrent reader
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = ruleSet.ProviderInfo()
					_ = ruleSet.Match(&adapter.InboundContext{Domain: "start.example"})
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
