package adapter

import (
	"context"
	"net/netip"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	N "github.com/sagernet/sing/common/network"

	"github.com/miekg/dns"
)

type DNSRouter interface {
	Lifecycle
	Exchange(ctx context.Context, message *dns.Msg, options DNSQueryOptions) (*dns.Msg, error)
	ExchangeAsync(ctx context.Context, message *dns.Msg, options DNSQueryOptions, callback func(response *dns.Msg, err error))
	Lookup(ctx context.Context, domain string, options DNSQueryOptions) ([]netip.Addr, error)
	ClearCache()
	LookupReverseMapping(ip netip.Addr) (string, bool)
	ResetNetwork()
}

type DNSClient interface {
	Start()
	Exchange(ctx context.Context, transport DNSTransport, message *dns.Msg, options DNSQueryOptions, responseChecker func(response *dns.Msg) bool) (*dns.Msg, error)
	ExchangeAsync(ctx context.Context, transport DNSTransport, message *dns.Msg, options DNSQueryOptions, responseChecker func(response *dns.Msg) bool, callback func(response *dns.Msg, err error))
	Lookup(ctx context.Context, transport DNSTransport, domain string, options DNSQueryOptions, responseChecker func(response *dns.Msg) bool) ([]netip.Addr, error)
	ClearCache()
}

type DNSQueryOptions struct {
	Transport              DNSTransport
	Strategy               C.DomainStrategy
	LookupStrategy         C.DomainStrategy
	DisableCache           bool
	DisableOptimisticCache bool
	RewriteTTL             *uint32
	Timeout                time.Duration
	ClientSubnet           netip.Prefix
	RemoveClientSubnet     bool
}

type RDRCStore interface {
	LoadRDRC(transportName string, qName string, qType uint16) (rejected bool)
	SaveRDRC(transportName string, qName string, qType uint16) error
	SaveRDRCAsync(transportName string, qName string, qType uint16, logger logger.Logger)
}

type DNSCacheStore interface {
	LoadDNSCache(transportName string, qName string, qType uint16) (rawMessage []byte, expireAt time.Time, loaded bool)
	SaveDNSCache(transportName string, qName string, qType uint16, rawMessage []byte, expireAt time.Time) error
	SaveDNSCacheAsync(transportName string, qName string, qType uint16, rawMessage []byte, expireAt time.Time, logger logger.Logger)
	DeleteDNSCache(transportName string, qName string, qType uint16, rawMessage []byte)
	ClearDNSCache() error
}

type DNSTransport interface {
	Lifecycle
	Type() string
	Tag() string
	Dependencies() []string
	// Reset closes the transport's existing connections so later requests use fresh connections.
	// Exchanges that are currently using those connections may fail.
	Reset()
	Exchange(ctx context.Context, message *dns.Msg) (*dns.Msg, error)
	ExchangeAsync(ctx context.Context, message *dns.Msg, callback func(response *dns.Msg, err error))
}

type DNSResponseChecker func(response *dns.Msg) bool

// DNSTransportWithResponseCheck lets a composite transport retry another
// member before returning a response that the DNS router would reject.
type DNSTransportWithResponseCheck interface {
	DNSTransport
	ExchangeWithResponseCheck(ctx context.Context, message *dns.Msg, checker DNSResponseChecker) (*dns.Msg, error)
}

type DNSTransportWithPreferredDomain interface {
	DNSTransport
	PreferredDomain(domain string) bool
}

type DNSTransportWithConfiguration interface {
	DNSTransport
	ServerAddresses() []netip.Addr
	SearchDomains() []string
}

type DNSTransportWithEnvironment interface {
	DNSTransport
	Environment() []string
}

type DNSTransportWithSearchDomain interface {
	DNSTransport
	HasSearchDomain() bool
}

// DNSTransportWithDialerOverride is implemented by transports that can be
// cloned with a different dialer (e.g., for group-level detour override).
type DNSTransportWithDialerOverride interface {
	DNSTransport
	RawDialer() N.Dialer
	WithDialer(dialer N.Dialer) DNSTransport
}

// DNSTransportNetworkless marks a transport that never opens network
// connections, so a group-level detour can safely leave it unchanged.
type DNSTransportNetworkless interface {
	DNSTransport
	Networkless()
}

type DNSGroupSnapshotProvider interface {
	DNSTransport
	GroupSnapshot() DNSGroupSnapshot
}

type DNSGroupSnapshot struct {
	Tag          string                   `json:"tag"`
	Policy       string                   `json:"policy"`
	Selection    string                   `json:"selection"`
	Execution    string                   `json:"execution"`
	MaxAttempts  int                      `json:"max_attempts"`
	MaxInflight  int                      `json:"max_inflight"`
	HedgeDelayMs int64                    `json:"hedge_delay_ms,omitempty"`
	Members      []DNSGroupMemberSnapshot `json:"members"`
}

type DNSGroupMemberSnapshot struct {
	Tag                 string    `json:"tag"`
	State               string    `json:"state"`
	AverageRTTMs        float64   `json:"average_rtt_ms"`
	JitterMs            float64   `json:"jitter_ms"`
	SuccessRate         float64   `json:"success_rate"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	TotalAttempts       uint64    `json:"total_attempts"`
	TotalFailures       uint64    `json:"total_failures"`
	Selected            uint64    `json:"selected"`
	Won                 uint64    `json:"won"`
	Inflight            int       `json:"inflight"`
	LastAttempt         time.Time `json:"last_attempt,omitempty"`
	LastSuccess         time.Time `json:"last_success,omitempty"`
	LastFailure         time.Time `json:"last_failure,omitempty"`
	CircuitUntil        time.Time `json:"circuit_until,omitempty"`
	ProbeAttempts       uint64    `json:"probe_attempts"`
	ProbeFailures       uint64    `json:"probe_failures"`
}

type DNSTransportRegistry interface {
	option.DNSTransportOptionsRegistry
	CreateDNSTransport(ctx context.Context, logger log.ContextLogger, tag string, transportType string, options any) (DNSTransport, error)
}

type DNSTransportManager interface {
	Lifecycle
	Transports() []DNSTransport
	Transport(tag string) (DNSTransport, bool)
	Default() DNSTransport
	FakeIP() FakeIPTransport
	Remove(tag string) error
	Create(ctx context.Context, logger log.ContextLogger, tag string, outboundType string, options any) error
}
