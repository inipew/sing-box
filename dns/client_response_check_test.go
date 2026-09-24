package dns

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/stretchr/testify/require"

	mDNS "github.com/miekg/dns"
)

type checkedClientTestTransport struct{}

func (*checkedClientTestTransport) Type() string                   { return "checked" }
func (*checkedClientTestTransport) Tag() string                    { return "checked" }
func (*checkedClientTestTransport) Dependencies() []string         { return nil }
func (*checkedClientTestTransport) Start(adapter.StartStage) error { return nil }
func (*checkedClientTestTransport) Close() error                   { return nil }
func (*checkedClientTestTransport) Reset()                         {}
func (t *checkedClientTestTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	return t.ExchangeWithResponseCheck(ctx, message, nil)
}
func (t *checkedClientTestTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	callback(t.Exchange(ctx, message))
}
func (*checkedClientTestTransport) ExchangeWithResponseCheck(_ context.Context, message *mDNS.Msg, checker adapter.DNSResponseChecker) (*mDNS.Msg, error) {
	response := new(mDNS.Msg)
	response.SetReply(message)
	if checker != nil && !checker(response) {
		return nil, ErrResponseRejected
	}
	return response, nil
}

func TestClientCallsCompositeResponseCheckerOnce(t *testing.T) {
	client := NewClient(ClientOptions{Context: context.Background(), DisableCache: true})
	message := new(mDNS.Msg)
	message.SetQuestion("example.com.", mDNS.TypeA)
	var calls atomic.Int32

	_, err := client.Exchange(context.Background(), &checkedClientTestTransport{}, message, adapter.DNSQueryOptions{}, func(*mDNS.Msg) bool {
		calls.Add(1)
		return true
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load())
}
