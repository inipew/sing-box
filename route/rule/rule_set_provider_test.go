package rule

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/stretchr/testify/require"
)

func TestLocalRuleSetProviderInfo(t *testing.T) {
	ruleSet, err := NewLocalRuleSet(context.Background(), log.NewNOPFactory().Logger(), "inline", option.RuleSet{
		Type: C.RuleSetTypeInline,
		InlineOptions: option.PlainRuleSet{
			Rules: []option.HeadlessRule{
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"one.example"}}},
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"two.example"}}},
			},
		},
	})
	require.NoError(t, err)

	info := ruleSet.ProviderInfo()
	require.Equal(t, C.RuleSetTypeInline, info.Type)
	require.Empty(t, info.Format)
	require.EqualValues(t, 2, info.RuleCount)
	require.True(t, info.UpdatedAt.IsZero())
}

func TestRemoteRuleSetUpdateReturnsFetchError(t *testing.T) {
	expectedErr := errors.New("network unavailable")
	ruleSet := newRemoteRuleSetForProviderTest(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, expectedErr
	}))

	err := ruleSet.Update(context.Background())

	require.ErrorIs(t, err, expectedErr)
}

func TestRemoteRuleSetUpdatesAreSerialized(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	ruleSet := newRemoteRuleSetForProviderTest(roundTripFunc(func(*http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-release
		return nil, errors.New("expected fetch failure")
	}))

	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	for range 2 {
		go func() {
			defer waitGroup.Done()
			_ = ruleSet.Update(context.Background())
		}()
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first update did not start")
	}
	select {
	case <-entered:
		t.Fatal("second update overlapped the first")
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("second update did not start after the first completed")
	}
	release <- struct{}{}
	waitGroup.Wait()
}

func TestLocalRuleSetSnapshotConsistencyAndCleanup(t *testing.T) {
	ruleSet, err := NewLocalRuleSet(context.Background(), log.NewNOPFactory().Logger(), "inline", option.RuleSet{
		Type: C.RuleSetTypeInline,
		InlineOptions: option.PlainRuleSet{
			Rules: []option.HeadlessRule{
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"one.example"}}},
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"two.example"}}},
			},
		},
	})
	require.NoError(t, err)

	info := ruleSet.ProviderInfo()
	require.EqualValues(t, 2, info.RuleCount)
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "one.example"}))

	ruleSet.Cleanup()
	require.False(t, ruleSet.Match(&adapter.InboundContext{Domain: "one.example"}))
	infoAfter := ruleSet.ProviderInfo()
	require.EqualValues(t, 2, infoAfter.RuleCount)
}

func TestLocalRuleSetFailedReloadMaintainsSnapshot(t *testing.T) {
	ruleSet, err := NewLocalRuleSet(context.Background(), log.NewNOPFactory().Logger(), "inline", option.RuleSet{
		Type: C.RuleSetTypeInline,
		InlineOptions: option.PlainRuleSet{
			Rules: []option.HeadlessRule{
				{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"one.example"}}},
			},
		},
	})
	require.NoError(t, err)

	initialInfo := ruleSet.ProviderInfo()
	require.EqualValues(t, 1, initialInfo.RuleCount)

	err = ruleSet.reloadRules([]option.HeadlessRule{
		{Type: "invalid-type"},
	})
	require.Error(t, err)

	afterInfo := ruleSet.ProviderInfo()
	require.EqualValues(t, 1, afterInfo.RuleCount)
	require.Equal(t, initialInfo.UpdatedAt, afterInfo.UpdatedAt)
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "one.example"}))
}

func TestRemoteRuleSetHTTP304UpdatesTimestampWithoutBreakingRules(t *testing.T) {
	initialTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	ruleSet := newRemoteRuleSetForProviderTest(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotModified,
			Status:     "304 Not Modified",
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	}))
	ruleSet.options.Format = C.RuleSetFormatSource

	err := ruleSet.loadBytesAt([]byte(`{"version":4,"rules":[{"domain":["one.example"]}]}`), initialTime)
	require.NoError(t, err)
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "one.example"}))
	require.Equal(t, initialTime, ruleSet.ProviderInfo().UpdatedAt)
	require.EqualValues(t, 1, ruleSet.ProviderInfo().RuleCount)

	ruleSet.IncRef()
	err = ruleSet.Update(context.Background())
	require.NoError(t, err)

	afterInfo := ruleSet.ProviderInfo()
	require.True(t, afterInfo.UpdatedAt.After(initialTime))
	require.EqualValues(t, 1, afterInfo.RuleCount)
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "one.example"}))
}

func TestRemoteRuleSetCleanupPreservesProviderInfo(t *testing.T) {
	timestamp := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	ruleSet := newRemoteRuleSetForProviderTest(nil)
	ruleSet.options.Format = C.RuleSetFormatSource

	err := ruleSet.loadBytesAt([]byte(`{"version":4,"rules":[{"domain":["one.example"]}]}`), timestamp)
	require.NoError(t, err)
	require.EqualValues(t, 1, ruleSet.ProviderInfo().RuleCount)
	require.Equal(t, timestamp, ruleSet.ProviderInfo().UpdatedAt)
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "one.example"}))

	ruleSet.Cleanup()

	require.False(t, ruleSet.Match(&adapter.InboundContext{Domain: "one.example"}))
	info := ruleSet.ProviderInfo()
	require.EqualValues(t, 1, info.RuleCount)
	require.Equal(t, timestamp, info.UpdatedAt)
}

func TestRemoteRuleSetFailedFetchMaintainsTimestamp(t *testing.T) {
	initialTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	ruleSet := newRemoteRuleSetForProviderTest(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network failure")
	}))
	ruleSet.options.Format = C.RuleSetFormatSource

	err := ruleSet.loadBytesAt([]byte(`{"version":4,"rules":[{"domain":["one.example"]}]}`), initialTime)
	require.NoError(t, err)

	err = ruleSet.Update(context.Background())
	require.Error(t, err)

	info := ruleSet.ProviderInfo()
	require.Equal(t, initialTime, info.UpdatedAt)
	require.EqualValues(t, 1, info.RuleCount)
	require.True(t, ruleSet.Match(&adapter.InboundContext{Domain: "one.example"}))
}

func TestConcurrentReloadAndProviderInfoNoTornRead(t *testing.T) {
	ruleSet := &LocalRuleSet{
		ctx:         context.Background(),
		logger:      log.NewNOPFactory().Logger(),
		tag:         "concurrent",
		ruleSetType: C.RuleSetTypeInline,
	}
	tA := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tB := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)

	rulesGenA := []option.HeadlessRule{
		{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"a1.example"}}},
		{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"a2.example"}}},
	}
	rulesGenB := []option.HeadlessRule{
		{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"b1.example"}}},
		{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"b2.example"}}},
		{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"b3.example"}}},
		{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{Domain: []string{"b4.example"}}},
	}

	err := ruleSet.reloadRulesAt(rulesGenA, tA)
	require.NoError(t, err)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		flip := false
		for {
			select {
			case <-stop:
				return
			default:
				if flip {
					_ = ruleSet.reloadRulesAt(rulesGenA, tA)
				} else {
					_ = ruleSet.reloadRulesAt(rulesGenB, tB)
				}
				flip = !flip
			}
		}
	}()

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					info := ruleSet.ProviderInfo()
					if info.RuleCount == 2 {
						require.True(t, info.UpdatedAt.Equal(tA), "expected tA for ruleCount 2")
					} else if info.RuleCount == 4 {
						require.True(t, info.UpdatedAt.Equal(tB), "expected tB for ruleCount 4")
					} else {
						t.Errorf("unexpected ruleCount: %d", info.RuleCount)
					}
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func newRemoteRuleSetForProviderTest(transport http.RoundTripper) *RemoteRuleSet {
	return &RemoteRuleSet{
		ctx:        context.Background(),
		logger:     log.NewNOPFactory().Logger(),
		tag:        "remote",
		url:        "https://rules.example/rules.srs",
		options:    option.RuleSet{Type: C.RuleSetTypeRemote, Format: C.RuleSetFormatBinary},
		httpClient: &http.Client{Transport: transport},
	}
}

type roundTripFunc func(request *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

var _ adapter.RuleSetProvider = (*LocalRuleSet)(nil)
var _ adapter.RuleSetProvider = (*RemoteRuleSet)(nil)
