package group

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
	"github.com/stretchr/testify/require"
)

type groupTestManager struct {
	adapter.OutboundManager
	member adapter.Outbound
}

type groupLifecycleOutbound struct{ selectorTestOutbound }

func (o *groupLifecycleOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, context.Canceled
}

func (m groupTestManager) Outbound(tag string) (adapter.Outbound, bool) {
	return m.member, tag == m.member.Tag()
}

func groupTestContext(member adapter.Outbound) context.Context {
	ctx := service.ContextWith[adapter.OutboundManager](context.Background(), groupTestManager{member: member})
	return service.ContextWithPtr(ctx, urltest.NewHistoryStorage())
}

func TestFallbackLifecycleResolvesMember(t *testing.T) {
	member := &groupLifecycleOutbound{selectorTestOutbound{tag: "member"}}
	ctx := groupTestContext(member)
	logger := log.NewNOPFactory().Logger()
	created, err := NewFallback(ctx, nil, logger, "fallback", option.FallbackOutboundOptions{Outbounds: []string{"member"}})
	require.NoError(t, err)
	lifecycle, ok := created.(adapter.Lifecycle)
	require.True(t, ok, "fallback must implement current lifecycle")
	scope := adapter.NewScope(ctx, logger)
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	require.NoError(t, lifecycle.Start(adapter.StartStateStart, scope))
	require.Same(t, member, created.(*Fallback).Selected("tcp"))
	require.NoError(t, lifecycle.Start(adapter.StartStateStarted, scope))
	require.True(t, created.(*Fallback).group.started)
	require.NoError(t, scope.Close())
	require.False(t, created.(*Fallback).group.started)
}

func TestLoadBalanceLifecycleResolvesMember(t *testing.T) {
	member := &groupLifecycleOutbound{selectorTestOutbound{tag: "member"}}
	ctx := groupTestContext(member)
	logger := log.NewNOPFactory().Logger()
	created, err := NewLoadBalance(ctx, nil, logger, "balance", option.LoadBalanceOutboundOptions{Outbounds: []string{"member"}})
	require.NoError(t, err)
	lifecycle, ok := created.(adapter.Lifecycle)
	require.True(t, ok, "load balance must implement current lifecycle")
	scope := adapter.NewScope(ctx, logger)
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	require.NoError(t, lifecycle.Start(adapter.StartStateStart, scope))
	require.Same(t, member, created.(*LoadBalance).Selected("tcp"))
	require.NoError(t, lifecycle.Start(adapter.StartStateStarted, scope))
	require.True(t, created.(*LoadBalance).group.started)
	require.NoError(t, scope.Close())
	require.False(t, created.(*LoadBalance).group.started)
}

type deadlineTestOutbound struct {
	selectorTestOutbound
	remaining chan time.Duration
}

func (o *deadlineTestOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	deadline, _ := ctx.Deadline()
	o.remaining <- time.Until(deadline)
	return nil, context.Canceled
}

func TestURLTestGroupUsesConfiguredTimeout(t *testing.T) {
	member := &deadlineTestOutbound{selectorTestOutbound: selectorTestOutbound{tag: "member"}, remaining: make(chan time.Duration, 1)}
	ctx := groupTestContext(member)
	group := &URLTestGroup{
		ctx:       ctx,
		outbound:  service.FromContext[adapter.OutboundManager](ctx),
		history:   service.PtrFromContext[urltest.HistoryStorage](ctx),
		logger:    log.NewNOPFactory().Logger(),
		outbounds: []adapter.Outbound{member},
		link:      "http://example.com/",
		timeout:   100 * time.Millisecond,
	}
	_, err := group.URLTest(ctx)
	require.NoError(t, err)
	remaining := <-member.remaining
	require.Greater(t, remaining, 0*time.Millisecond)
	require.LessOrEqual(t, remaining, 100*time.Millisecond)
}
