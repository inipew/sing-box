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

type multiGroupTestManager struct {
	adapter.OutboundManager
	members map[string]adapter.Outbound
}

func (m multiGroupTestManager) Outbound(tag string) (adapter.Outbound, bool) {
	outbound, loaded := m.members[tag]
	return outbound, loaded
}

func multiGroupTestContext(members map[string]adapter.Outbound, history *urltest.HistoryStorage) context.Context {
	ctx := service.ContextWith[adapter.OutboundManager](context.Background(), multiGroupTestManager{members: members})
	return service.ContextWithPtr(ctx, history)
}

func TestFallbackReferences(t *testing.T) {
	node1 := &groupLifecycleOutbound{selectorTestOutbound{tag: "node1"}}
	node2 := &groupLifecycleOutbound{selectorTestOutbound{tag: "node2"}}
	history := urltest.NewHistoryStorage()
	ctx := multiGroupTestContext(map[string]adapter.Outbound{"node1": node1, "node2": node2}, history)
	logger := log.NewNOPFactory().Logger()
	created, err := NewFallback(ctx, nil, logger, "fallback", option.FallbackOutboundOptions{Outbounds: []string{"node1", "node2"}})
	require.NoError(t, err)

	referrer, ok := created.(adapter.Referrer)
	require.True(t, ok, "fallback must implement adapter.Referrer")

	scope := adapter.NewScope(ctx, logger)
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	require.NoError(t, created.(adapter.Lifecycle).Start(adapter.StartStateStart, scope))

	// Without history, first member is selected as fallback
	require.Equal(t, []string{"node1"}, referrer.References())

	// node2 is healthy, node1 is not
	history.StoreURLTestHistory("node2", &adapter.URLTestHistory{Time: time.Now(), Delay: 50})
	require.Equal(t, []string{"node2"}, referrer.References())

	// node1 becomes healthy as well, takes priority over node2
	history.StoreURLTestHistory("node1", &adapter.URLTestHistory{Time: time.Now(), Delay: 100})
	require.Equal(t, []string{"node1"}, referrer.References())
}

func TestLoadBalanceReferences(t *testing.T) {
	node1 := &groupLifecycleOutbound{selectorTestOutbound{tag: "node1"}}
	node2 := &groupLifecycleOutbound{selectorTestOutbound{tag: "node2"}}
	node3 := &groupLifecycleOutbound{selectorTestOutbound{tag: "node3"}}
	history := urltest.NewHistoryStorage()
	ctx := multiGroupTestContext(map[string]adapter.Outbound{"node1": node1, "node2": node2, "node3": node3}, history)
	logger := log.NewNOPFactory().Logger()
	created, err := NewLoadBalance(ctx, nil, logger, "balance", option.LoadBalanceOutboundOptions{Outbounds: []string{"node1", "node2", "node3"}})
	require.NoError(t, err)

	referrer, ok := created.(adapter.Referrer)
	require.True(t, ok, "load balance must implement adapter.Referrer")

	scope := adapter.NewScope(ctx, logger)
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	require.NoError(t, created.(adapter.Lifecycle).Start(adapter.StartStateStart, scope))

	// Without history, fallback returns the first member
	require.Equal(t, []string{"node1"}, referrer.References())

	// node1 and node3 are healthy, node2 is not
	history.StoreURLTestHistory("node1", &adapter.URLTestHistory{Time: time.Now(), Delay: 50})
	history.StoreURLTestHistory("node3", &adapter.URLTestHistory{Time: time.Now(), Delay: 60})
	require.Equal(t, []string{"node1", "node3"}, referrer.References())

	// All nodes are healthy
	history.StoreURLTestHistory("node2", &adapter.URLTestHistory{Time: time.Now(), Delay: 70})
	require.ElementsMatch(t, []string{"node1", "node2", "node3"}, referrer.References())
}
