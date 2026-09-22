package dns

import (
	"sync"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/stretchr/testify/require"
)

func TestDNSRuleInfoReturnsStableSnapshot(t *testing.T) {
	rule := &dnsRuleInfoTestRule{ruleType: "default", payload: "domain=example.com", action: "server=local"}
	router := &Router{rules: []adapter.DNSRule{rule}}

	info := router.DNSRuleInfo()
	rule.payload = "changed"

	require.Equal(t, []adapter.RuleInfo{{
		Type:    "default",
		Payload: "domain=example.com",
		Action:  "server=local",
	}}, info)
}

func TestDNSRuleInfoConcurrentReplacement(t *testing.T) {
	router := &Router{}
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		for range 100 {
			router.rulesAccess.Lock()
			router.rules = []adapter.DNSRule{&dnsRuleInfoTestRule{ruleType: "default", payload: "domain=example.com", action: "server=local"}}
			router.rulesAccess.Unlock()
		}
	}()
	go func() {
		defer waitGroup.Done()
		for range 100 {
			_ = router.DNSRuleInfo()
		}
	}()
	waitGroup.Wait()
}

type dnsRuleInfoTestRule struct {
	adapter.DNSRule
	ruleType string
	payload  string
	action   string
}

func (r *dnsRuleInfoTestRule) Type() string {
	return r.ruleType
}

func (r *dnsRuleInfoTestRule) String() string {
	return r.payload
}

func (r *dnsRuleInfoTestRule) Action() adapter.RuleAction {
	return dnsRuleInfoTestAction(r.action)
}

type dnsRuleInfoTestAction string

func (a dnsRuleInfoTestAction) Type() string {
	return "test"
}

func (a dnsRuleInfoTestAction) String() string {
	return string(a)
}
