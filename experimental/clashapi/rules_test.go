package clashapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/stretchr/testify/require"
)

func TestRulesIncludesDNSRuleSnapshotsBeforeRouteRules(t *testing.T) {
	router := ruleRouter(
		&ruleListTestRouter{rules: []adapter.Rule{&ruleListTestRule{ruleType: "route", payload: "network=tcp", action: "proxy"}}},
		&ruleListTestDNSRouter{rules: []adapter.RuleInfo{{Type: "dns", Payload: "domain=example.com", Action: "server=local"}}},
	)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var body struct {
		Rules []Rule `json:"rules"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, []Rule{
		{Type: "dns", Payload: "domain=example.com", Proxy: "server=local"},
		{Type: "route", Payload: "network=tcp", Proxy: "proxy"},
	}, body.Rules)
}

type ruleListTestRouter struct {
	rules []adapter.Rule
}

func (r *ruleListTestRouter) Rules() []adapter.Rule {
	return r.rules
}

func (r *ruleListTestRouter) Rule(uuid string) (adapter.Rule, bool) {
	return nil, false
}

type ruleListTestDNSRouter struct {
	rules []adapter.RuleInfo
}

func (r *ruleListTestDNSRouter) DNSRuleInfo() []adapter.RuleInfo {
	return r.rules
}

type ruleListTestRule struct {
	adapter.Rule
	ruleType string
	payload  string
	action   string
}

func (r *ruleListTestRule) Type() string {
	return r.ruleType
}

func (r *ruleListTestRule) String() string {
	return r.payload
}

func (r *ruleListTestRule) Action() adapter.RuleAction {
	return ruleListTestAction(r.action)
}

func (r *ruleListTestRule) Disabled() bool {
	return false
}

func (r *ruleListTestRule) UUID() string {
	return ""
}

type ruleListTestAction string

func (a ruleListTestAction) Type() string {
	return "test"
}

func (a ruleListTestAction) String() string {
	return string(a)
}
