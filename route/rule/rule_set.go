package rule

import (
	"context"
	"reflect"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"

	"go4.org/netipx"
)

func NewRuleSet(ctx context.Context, logger logger.ContextLogger, tag string, options option.RuleSet) (adapter.RuleSet, error) {
	switch options.Type {
	case C.RuleSetTypeInline, C.RuleSetTypeLocal, "":
		return NewLocalRuleSet(ctx, logger, tag, options)
	case C.RuleSetTypeRemote:
		return NewRemoteRuleSet(ctx, logger, tag, options)
	default:
		return nil, E.New("unknown rule-set type: ", options.Type)
	}
}

func extractIPSetFromRule(rawRule adapter.HeadlessRule) []*netipx.IPSet {
	switch rule := rawRule.(type) {
	case *DefaultHeadlessRule:
		return common.FlatMap(rule.destinationIPCIDRItems, func(rawItem RuleItem) []*netipx.IPSet {
			switch item := rawItem.(type) {
			case *IPCIDRItem:
				return []*netipx.IPSet{item.ipSet.IPSet()}
			default:
				return nil
			}
		})
	case *LogicalHeadlessRule:
		return common.FlatMap(rule.rules, extractIPSetFromRule)
	default:
		panic("unexpected rule type")
	}
}

func HasHeadlessRule(rules []option.HeadlessRule, cond func(rule option.DefaultHeadlessRule) bool) bool {
	for _, rule := range rules {
		switch rule.Type {
		case C.RuleTypeDefault:
			if cond(rule.DefaultOptions) {
				return true
			}
		case C.RuleTypeLogical:
			if HasHeadlessRule(rule.LogicalOptions.Rules, cond) {
				return true
			}
		}
	}
	return false
}

func isProcessHeadlessRule(rule option.DefaultHeadlessRule) bool {
	return len(rule.ProcessName) > 0 || len(rule.ProcessPath) > 0 || len(rule.ProcessPathRegex) > 0 || len(rule.PackageName) > 0 || len(rule.PackageNameRegex) > 0
}

func isWIFIHeadlessRule(rule option.DefaultHeadlessRule) bool {
	return len(rule.WIFISSID) > 0 || len(rule.WIFIBSSID) > 0
}

func isIPCIDRHeadlessRule(rule option.DefaultHeadlessRule) bool {
	return len(rule.IPCIDR) > 0 || rule.IPSet != nil
}

func isDNSQueryTypeHeadlessRule(rule option.DefaultHeadlessRule) bool {
	return len(rule.QueryType) > 0
}

func isNonIPCIDRHeadlessRule(rule option.DefaultHeadlessRule) bool {
	ipOnly := option.DefaultHeadlessRule{
		IPCIDR: rule.IPCIDR,
		IPSet:  rule.IPSet,
		Invert: rule.Invert,
	}
	return !reflect.DeepEqual(rule, ipOnly)
}

func buildRuleSetMetadata(headlessRules []option.HeadlessRule) adapter.RuleSetMetadata {
	return adapter.RuleSetMetadata{
		ContainsProcessRule:      HasHeadlessRule(headlessRules, isProcessHeadlessRule),
		ContainsWIFIRule:         HasHeadlessRule(headlessRules, isWIFIHeadlessRule),
		ContainsIPCIDRRule:       HasHeadlessRule(headlessRules, isIPCIDRHeadlessRule),
		ContainsDNSQueryTypeRule: HasHeadlessRule(headlessRules, isDNSQueryTypeHeadlessRule),
		ContainsNonIPCIDRRule:    HasHeadlessRule(headlessRules, isNonIPCIDRHeadlessRule),
	}
}

func validateRuleSetMetadataUpdate(ctx context.Context, tag string, metadata adapter.RuleSetMetadata) error {
	validator := service.FromContext[adapter.DNSRuleSetUpdateValidator](ctx)
	if validator == nil {
		return nil
	}
	return validator.ValidateRuleSetMetadataUpdate(tag, metadata)
}

func CountHeadlessRules(rules []option.HeadlessRule) uint64 {
	var count uint64
	for _, rule := range rules {
		switch rule.Type {
		case C.RuleTypeDefault, "":
			r := rule.DefaultOptions
			count += uint64(len(r.QueryType) +
				len(r.Network) +
				len(r.Domain) +
				len(r.DomainSuffix) +
				len(r.DomainKeyword) +
				len(r.DomainRegex) +
				len(r.SourceIPCIDR) +
				len(r.IPCIDR) +
				len(r.SourcePort) +
				len(r.SourcePortRange) +
				len(r.Port) +
				len(r.PortRange) +
				len(r.ProcessName) +
				len(r.ProcessPath) +
				len(r.ProcessPathRegex) +
				len(r.PackageName) +
				len(r.PackageNameRegex) +
				len(r.NetworkType) +
				len(r.WIFISSID) +
				len(r.WIFIBSSID) +
				len(r.AdGuardDomain))
		case C.RuleTypeLogical:
			count += CountHeadlessRules(rule.LogicalOptions.Rules)
		}
	}
	return count
}
