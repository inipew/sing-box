package adapter

import (
	C "github.com/sagernet/sing-box/constant"

	"github.com/miekg/dns"
)

type HeadlessRule interface {
	Match(metadata *InboundContext) bool
	String() string
}

type Rule interface {
	HeadlessRule
	SimpleLifecycle
	Disabled() bool
	UUID() string
	ChangeStatus()
	Type() string
	Action() RuleAction
}

type DNSRule interface {
	Rule
	LegacyPreMatch(metadata *InboundContext) bool
	WithAddressLimit() bool
	MatchAddressLimit(metadata *InboundContext, response *dns.Msg) bool
	MatchResponseTag() string
	MatchResponseTags() []string
	MatchResponseAnonymous() bool
	Race() bool
}

type RuleAction interface {
	Type() string
	String() string
}

type RuleInfo struct {
	Type    string
	Payload string
	Action  string
}

type DNSRuleInfoProvider interface {
	DNSRuleInfo() []RuleInfo
}

func IsFinalAction(action RuleAction) bool {
	switch action.Type() {
	case C.RuleActionTypeSniff, C.RuleActionTypeResolve, C.RuleActionTypeEvaluate:
		return false
	default:
		return true
	}
}
