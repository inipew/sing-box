package group

import (
	"context"
	"errors"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/stretchr/testify/require"

	mDNS "github.com/miekg/dns"
)

type closeErrorTransport struct {
	tag string
	err error
}

func (t *closeErrorTransport) Type() string                   { return "test" }
func (t *closeErrorTransport) Tag() string                    { return t.tag }
func (t *closeErrorTransport) Dependencies() []string         { return nil }
func (t *closeErrorTransport) Start(adapter.StartStage) error { return nil }
func (t *closeErrorTransport) Close() error                   { return t.err }
func (t *closeErrorTransport) Reset()                         {}
func (t *closeErrorTransport) Exchange(context.Context, *mDNS.Msg) (*mDNS.Msg, error) {
	return nil, nil
}
func (t *closeErrorTransport) ExchangeAsync(_ context.Context, _ *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	callback(nil, nil)
}

func TestCloseOwnedTransportsPreservesPrimaryAndCleanupErrors(t *testing.T) {
	startErr := errors.New("start failed")
	closeErr := errors.New("close failed")

	err := closeOwnedTransports(startErr, []adapter.DNSTransport{
		&closeErrorTransport{tag: "member", err: closeErr},
	})

	require.ErrorIs(t, err, startErr)
	require.ErrorIs(t, err, closeErr)
	require.ErrorContains(t, err, "close cloned DNS transport member")
}
