package option

import (
	"context"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/json"

	"github.com/stretchr/testify/require"
)

func TestDNSRuleActionRespondUnmarshalJSON(t *testing.T) {
	t.Parallel()

	var action DNSRuleAction
	err := json.UnmarshalContext(context.Background(), []byte(`{"action":"respond"}`), &action)
	require.NoError(t, err)
	require.Equal(t, C.RuleActionTypeRespond, action.Action)
	require.Equal(t, DNSRouteActionOptions{}, action.RouteOptions)
}

func TestDNSRuleActionRespondRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	var action DNSRuleAction
	err := json.UnmarshalContext(context.Background(), []byte(`{"action":"respond","disable_cache":true}`), &action)
	require.ErrorContains(t, err, "unknown field")
}

func TestRateLimitActionOptionsUnmarshalJSON(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// 1. Tag format
	var action1 RuleAction
	err := json.UnmarshalContext(ctx, []byte(`{"action":"route","outbound":"direct","rate_limit":"streaming"}`), &action1)
	require.NoError(t, err)
	require.NotNil(t, action1.RouteOptions.RateLimit)
	require.Equal(t, "streaming", action1.RouteOptions.RateLimit.Tag)
	require.Nil(t, action1.RouteOptions.RateLimit.Upload)
	require.Nil(t, action1.RouteOptions.RateLimit.Download)

	// 2. Inline format
	var action2 RuleAction
	err = json.UnmarshalContext(ctx, []byte(`{"action":"route-options","rate_limit":{"upload":"1 MB","download":"5 MB"}}`), &action2)
	require.NoError(t, err)
	require.NotNil(t, action2.RouteOptionsOptions.RateLimit)
	require.Equal(t, "", action2.RouteOptionsOptions.RateLimit.Tag)
	require.Equal(t, uint64(1000*1000), action2.RouteOptionsOptions.RateLimit.Upload.Value())
	require.Equal(t, uint64(5*1000*1000), action2.RouteOptionsOptions.RateLimit.Download.Value())

	// 3. Rejection: empty tag
	var action3 RuleAction
	err = json.UnmarshalContext(ctx, []byte(`{"action":"route","rate_limit":""}`), &action3)
	require.ErrorContains(t, err, "empty rate_limit tag")

	// 4. Rejection: empty object
	var opt4 RateLimitActionOptions
	err = json.UnmarshalContext(ctx, []byte(`{}`), &opt4)
	require.ErrorContains(t, err, "empty rate_limit option")

	// 5. Rejection: tag + inline combination
	var opt5 RateLimitActionOptions
	err = json.UnmarshalContext(ctx, []byte(`{"tag":"streaming","upload":"1 MB"}`), &opt5)
	require.ErrorContains(t, err, "cannot specify both 'tag' and inline")
}
