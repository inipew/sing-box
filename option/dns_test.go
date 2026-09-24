package option

import (
	"context"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

type stubDNSTransportOptionsRegistry struct{}

func (stubDNSTransportOptionsRegistry) OptionTypes() []string {
	return []string{C.DNSTypeUDP, C.DNSTypeFakeIP, C.DNSTypeGroup}
}

func (stubDNSTransportOptionsRegistry) CreateOptions(transportType string) (any, bool) {
	switch transportType {
	case C.DNSTypeUDP:
		return new(RemoteDNSServerOptions), true
	case C.DNSTypeFakeIP:
		return new(FakeIPDNSServerOptions), true
	case C.DNSTypeGroup:
		return new(GroupDNSServerOptions), true
	default:
		return nil, false
	}
}

func TestGroupDNSOptionsRejectLegacyFields(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		field string
		value string
	}{
		{"strategy", `"wp2"`},
		{"mode", `"sequential"`},
		{"fallback_delay", `"300ms"`},
		{"max_retries", `2`},
		{"health_check", `{}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.field, func(t *testing.T) {
			var options GroupDNSServerOptions
			err := json.Unmarshal([]byte(`{"servers":["a"],"`+testCase.field+`":`+testCase.value+`}`), &options)
			require.EqualError(t, err, "legacy DNS group field `"+testCase.field+"` has been removed; use `policy` and `advanced` instead")
		})
	}
}

func TestGroupDNSOptionsPolicyDefaultsAndValidation(t *testing.T) {
	t.Parallel()

	var options GroupDNSServerOptions
	require.NoError(t, json.Unmarshal([]byte(`{"servers":["a","b"]}`), &options))
	require.Equal(t, "reliable", options.Policy)

	err := json.Unmarshal([]byte(`{
		"servers":["a","b"],
		"policy":"privacy",
		"advanced":{"execution":"failover","max_inflight":2}
	}`), &options)
	require.EqualError(t, err, "dns group advanced.max_inflight must be 1 when execution is failover")
}

func TestDNSOptionsRejectsLegacyFakeIPOptions(t *testing.T) {
	t.Parallel()

	ctx := service.ContextWith[DNSTransportOptionsRegistry](context.Background(), stubDNSTransportOptionsRegistry{})
	var options DNSOptions
	err := json.UnmarshalContext(ctx, []byte(`{
		"fakeip": {
			"enabled": true,
			"inet4_range": "198.18.0.0/15"
		}
	}`), &options)
	require.EqualError(t, err, legacyDNSFakeIPRemovedMessage)
}

func TestDNSServerOptionsRejectsLegacyFormats(t *testing.T) {
	t.Parallel()

	ctx := service.ContextWith[DNSTransportOptionsRegistry](context.Background(), stubDNSTransportOptionsRegistry{})
	testCases := []string{
		`{"address":"1.1.1.1"}`,
		`{"type":"legacy","address":"1.1.1.1"}`,
	}
	for _, content := range testCases {
		var options DNSServerOptions
		err := json.UnmarshalContext(ctx, []byte(content), &options)
		require.EqualError(t, err, legacyDNSServerRemovedMessage)
	}
}
