package clashapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/stretchr/testify/require"

	mDNS "github.com/miekg/dns"
)

type dnsGroupTestManager struct {
	transports []adapter.DNSTransport
}

func (m *dnsGroupTestManager) Start(adapter.StartStage) error { return nil }
func (m *dnsGroupTestManager) Close() error                   { return nil }
func (m *dnsGroupTestManager) Transports() []adapter.DNSTransport {
	return m.transports
}
func (m *dnsGroupTestManager) Transport(tag string) (adapter.DNSTransport, bool) {
	for _, transport := range m.transports {
		if transport.Tag() == tag {
			return transport, true
		}
	}
	return nil, false
}
func (m *dnsGroupTestManager) Default() adapter.DNSTransport   { return nil }
func (m *dnsGroupTestManager) FakeIP() adapter.FakeIPTransport { return nil }
func (m *dnsGroupTestManager) Remove(string) error             { return nil }
func (m *dnsGroupTestManager) Create(context.Context, log.ContextLogger, string, string, any) error {
	return nil
}

type dnsGroupTestTransport struct{}

func (*dnsGroupTestTransport) Type() string                   { return "group" }
func (*dnsGroupTestTransport) Tag() string                    { return "primary" }
func (*dnsGroupTestTransport) Dependencies() []string         { return []string{"a"} }
func (*dnsGroupTestTransport) Start(adapter.StartStage) error { return nil }
func (*dnsGroupTestTransport) Close() error                   { return nil }
func (*dnsGroupTestTransport) Reset()                         {}
func (*dnsGroupTestTransport) Exchange(context.Context, *mDNS.Msg) (*mDNS.Msg, error) {
	return nil, nil
}
func (*dnsGroupTestTransport) ExchangeAsync(_ context.Context, _ *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	callback(nil, nil)
}
func (*dnsGroupTestTransport) GroupSnapshot() adapter.DNSGroupSnapshot {
	return adapter.DNSGroupSnapshot{Tag: "primary", Policy: "reliable", Members: []adapter.DNSGroupMemberSnapshot{{Tag: "a", State: "closed"}}}
}

func TestDNSGroupRouterListDetailAndNotFound(t *testing.T) {
	manager := &dnsGroupTestManager{transports: []adapter.DNSTransport{&dnsGroupTestTransport{}}}
	handler := dnsGroupRouter(manager)

	for _, testCase := range []struct {
		path   string
		status int
		body   string
	}{
		{"/", http.StatusOK, `"policy":"reliable"`},
		{"/primary", http.StatusOK, `"tag":"primary"`},
		{"/missing", http.StatusNotFound, `"message":"DNS group not found"`},
	} {
		request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, testCase.status, response.Code)
		require.Contains(t, response.Body.String(), testCase.body)
		require.NotContains(t, response.Body.String(), "qname")
	}
}
