package rule

import (
	"context"
	"errors"
	"net/http"
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
