package adguard

import (
	"bufio"
	"bytes"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

type agdguardRuleLine struct {
	ruleLine    string
	isRawDomain bool
	isIPCIDR    bool
	isExclude   bool
	isSuffix    bool
	hasStart    bool
	hasEnd      bool
	isRegexp    bool
	isImportant bool
}

func parseIPOrCIDR(s string) (string, bool) {
	if prefix, err := netip.ParsePrefix(s); err == nil {
		return prefix.String(), true
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.String(), true
	}
	if prefix, err := parseADGuardIPCIDRLine(s); err == nil {
		return prefix.String(), true
	}
	return "", false
}

func ToOptions(reader io.Reader, logger logger.Logger) ([]option.HeadlessRule, error) {
	scanner := bufio.NewScanner(reader)
	var (
		ruleLines    []agdguardRuleLine
		ignoredLines int
	)
parseLine:
	for scanner.Scan() {
		ruleLine := scanner.Text()
		if ruleLine == "" {
			continue
		}
		if strings.HasPrefix(ruleLine, "!") || strings.HasPrefix(ruleLine, "#") {
			continue
		}
		originRuleLine := ruleLine
		tmpRuleLine := ruleLine
		tmpRuleLine = strings.TrimSuffix(tmpRuleLine, "|")
		if strings.HasPrefix(tmpRuleLine, "||") {
			tmpRuleLine = tmpRuleLine[2:]
		} else if strings.HasPrefix(tmpRuleLine, "|") {
			tmpRuleLine = tmpRuleLine[1:]
		}
		if strings.HasSuffix(tmpRuleLine, "^") {
			tmpRuleLine = tmpRuleLine[:len(tmpRuleLine)-1]
		}
		if ipStr, ok := parseIPOrCIDR(tmpRuleLine); ok {
			ruleLines = append(ruleLines, agdguardRuleLine{
				ruleLine: ipStr,
				isIPCIDR: true,
			})
			continue
		}
		if M.IsDomainName(ruleLine) {
			ruleLines = append(ruleLines, agdguardRuleLine{
				ruleLine:    ruleLine,
				isRawDomain: true,
			})
			continue
		}
		hostLine, err := parseAdGuardHostLine(ruleLine)
		if err == nil {
			if hostLine != "" {
				ruleLines = append(ruleLines, agdguardRuleLine{
					ruleLine:    hostLine,
					isRawDomain: true,
					hasStart:    true,
					hasEnd:      true,
				})
			}
			continue
		}
		ruleLine = strings.TrimSuffix(ruleLine, "|")
		var (
			isExclude   bool
			isSuffix    bool
			hasStart    bool
			hasEnd      bool
			isRegexp    bool
			isImportant bool
		)
		if !strings.HasPrefix(ruleLine, "/") && strings.Contains(ruleLine, "$") {
			params := common.SubstringAfter(ruleLine, "$")
			for param := range strings.SplitSeq(params, ",") {
				paramParts := strings.Split(param, "=")
				var ignored bool
				if len(paramParts) > 0 && len(paramParts) <= 2 {
					switch paramParts[0] {
					case "app", "network":
						// maybe support by package_name/process_name
					case "dnstype":
						// maybe support by query_type
					case "important":
						ignored = true
						isImportant = true
					case "dnsrewrite":
						if len(paramParts) == 2 && M.ParseAddr(paramParts[1]).IsUnspecified() {
							ignored = true
						}
					}
				}
				if !ignored {
					ignoredLines++
					logger.Debug("ignored unsupported rule with modifier: ", paramParts[0], ": ", originRuleLine)
					continue parseLine
				}
			}
			ruleLine = common.SubstringBefore(ruleLine, "$")
		}
		if strings.HasPrefix(ruleLine, "@@") {
			ruleLine = ruleLine[2:]
			isExclude = true
		}
		tmpRuleLine = ruleLine
		tmpRuleLine = strings.TrimSuffix(tmpRuleLine, "|")
		if strings.HasPrefix(tmpRuleLine, "||") {
			tmpRuleLine = tmpRuleLine[2:]
		} else if strings.HasPrefix(tmpRuleLine, "|") {
			tmpRuleLine = tmpRuleLine[1:]
		}
		if strings.HasSuffix(tmpRuleLine, "^") {
			tmpRuleLine = tmpRuleLine[:len(tmpRuleLine)-1]
		}
		if ipStr, ok := parseIPOrCIDR(tmpRuleLine); ok {
			ruleLines = append(ruleLines, agdguardRuleLine{
				ruleLine:    ipStr,
				isIPCIDR:    true,
				isExclude:   isExclude,
				isImportant: isImportant,
			})
			continue
		}
		ruleLine = strings.TrimSuffix(ruleLine, "|")
		if strings.HasPrefix(ruleLine, "||") {
			ruleLine = ruleLine[2:]
			isSuffix = true
		} else if strings.HasPrefix(ruleLine, "|") {
			ruleLine = ruleLine[1:]
			hasStart = true
		}
		if strings.HasSuffix(ruleLine, "^") {
			ruleLine = ruleLine[:len(ruleLine)-1]
			hasEnd = true
		}
		if strings.HasPrefix(ruleLine, "/") && strings.HasSuffix(ruleLine, "/") {
			ruleLine = ruleLine[1 : len(ruleLine)-1]
			if ignoreIPCIDRRegexp(ruleLine) {
				ignoredLines++
				logger.Debug("ignored unsupported rule with IPCIDR regexp: ", originRuleLine)
				continue
			}
			isRegexp = true
		} else {
			if strings.Contains(ruleLine, "://") {
				ruleLine = common.SubstringAfter(ruleLine, "://")
				isSuffix = true
			}
			if strings.Contains(ruleLine, "/") {
				ignoredLines++
				logger.Debug("ignored unsupported rule with path: ", originRuleLine)
				continue
			}
			if strings.Contains(ruleLine, "?") || strings.Contains(ruleLine, "&") {
				ignoredLines++
				logger.Debug("ignored unsupported rule with query: ", originRuleLine)
				continue
			}
			if strings.Contains(ruleLine, "[") || strings.Contains(ruleLine, "]") ||
				strings.Contains(ruleLine, "(") || strings.Contains(ruleLine, ")") ||
				strings.Contains(ruleLine, "!") || strings.Contains(ruleLine, "#") {
				ignoredLines++
				logger.Debug("ignored unsupported cosmetic filter: ", originRuleLine)
				continue
			}
			if strings.Contains(ruleLine, "~") {
				ignoredLines++
				logger.Debug("ignored unsupported rule modifier: ", originRuleLine)
				continue
			}
			var domainCheck string
			if strings.HasPrefix(ruleLine, ".") || strings.HasPrefix(ruleLine, "-") {
				domainCheck = "r" + ruleLine
			} else {
				domainCheck = ruleLine
			}
			if ruleLine == "" {
				ignoredLines++
				logger.Debug("ignored unsupported rule with empty domain", originRuleLine)
				continue
			} else {
				domainCheck = strings.ReplaceAll(domainCheck, "*", "x")
				if !M.IsDomainName(domainCheck) {
					_, ipErr := parseADGuardIPCIDRLine(ruleLine)
					if ipErr == nil {
						ignoredLines++
						logger.Debug("ignored unsupported rule with IPCIDR: ", originRuleLine)
						continue
					}
					if M.ParseSocksaddr(domainCheck).Port != 0 {
						logger.Debug("ignored unsupported rule with port: ", originRuleLine)
					} else {
						logger.Debug("ignored unsupported rule with invalid domain: ", originRuleLine)
					}
					ignoredLines++
					continue
				}
			}
		}
		ruleLines = append(ruleLines, agdguardRuleLine{
			ruleLine:    ruleLine,
			isExclude:   isExclude,
			isSuffix:    isSuffix,
			hasStart:    hasStart,
			hasEnd:      hasEnd,
			isRegexp:    isRegexp,
			isImportant: isImportant,
		})
	}
	if len(ruleLines) == 0 {
		return nil, E.New("AdGuard rule-set is empty or all rules are unsupported")
	}
	if common.All(ruleLines, func(it agdguardRuleLine) bool {
		return it.isRawDomain
	}) {
		return []option.HeadlessRule{
			{
				Type: C.RuleTypeDefault,
				DefaultOptions: option.DefaultHeadlessRule{
					Domain: common.Map(ruleLines, func(it agdguardRuleLine) string {
						return it.ruleLine
					}),
				},
			},
		}, nil
	}
	mapDomain := func(it agdguardRuleLine) string {
		ruleLine := it.ruleLine
		if it.isSuffix {
			ruleLine = "||" + ruleLine
		} else if it.hasStart {
			ruleLine = "|" + ruleLine
		}
		if it.hasEnd {
			ruleLine += "^"
		}
		return ruleLine
	}

	importantDomain := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return it.isImportant && !it.isRegexp && !it.isExclude && !it.isIPCIDR }), mapDomain)
	importantDomainRegex := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return it.isImportant && it.isRegexp && !it.isExclude && !it.isIPCIDR }), mapDomain)
	importantExcludeDomain := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return it.isImportant && !it.isRegexp && it.isExclude && !it.isIPCIDR }), mapDomain)
	importantExcludeDomainRegex := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return it.isImportant && it.isRegexp && it.isExclude && !it.isIPCIDR }), mapDomain)
	domain := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return !it.isImportant && !it.isRegexp && !it.isExclude && !it.isIPCIDR }), mapDomain)
	domainRegex := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return !it.isImportant && it.isRegexp && !it.isExclude && !it.isIPCIDR }), mapDomain)
	excludeDomain := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return !it.isImportant && !it.isRegexp && it.isExclude && !it.isIPCIDR }), mapDomain)
	excludeDomainRegex := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return !it.isImportant && it.isRegexp && it.isExclude && !it.isIPCIDR }), mapDomain)

	importantIPCIDR := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return it.isImportant && it.isIPCIDR && !it.isExclude }), func(it agdguardRuleLine) string { return it.ruleLine })
	importantExcludeIPCIDR := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return it.isImportant && it.isIPCIDR && it.isExclude }), func(it agdguardRuleLine) string { return it.ruleLine })
	ipCIDR := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return !it.isImportant && it.isIPCIDR && !it.isExclude }), func(it agdguardRuleLine) string { return it.ruleLine })
	excludeIPCIDR := common.Map(common.Filter(ruleLines, func(it agdguardRuleLine) bool { return !it.isImportant && it.isIPCIDR && it.isExclude }), func(it agdguardRuleLine) string { return it.ruleLine })

	currentRule := option.HeadlessRule{
		Type: C.RuleTypeDefault,
		DefaultOptions: option.DefaultHeadlessRule{
			AdGuardDomain: domain,
			DomainRegex:   domainRegex,
			IPCIDR:        ipCIDR,
		},
	}
	if len(excludeDomain) > 0 || len(excludeDomainRegex) > 0 || len(excludeIPCIDR) > 0 {
		currentRule = option.HeadlessRule{
			Type: C.RuleTypeLogical,
			LogicalOptions: option.LogicalHeadlessRule{
				Mode: C.LogicalTypeAnd,
				Rules: []option.HeadlessRule{
					{
						Type: C.RuleTypeDefault,
						DefaultOptions: option.DefaultHeadlessRule{
							AdGuardDomain: excludeDomain,
							DomainRegex:   excludeDomainRegex,
							IPCIDR:        excludeIPCIDR,
							Invert:        true,
						},
					},
					currentRule,
				},
			},
		}
	}
	if len(importantDomain) > 0 || len(importantDomainRegex) > 0 || len(importantIPCIDR) > 0 {
		currentRule = option.HeadlessRule{
			Type: C.RuleTypeLogical,
			LogicalOptions: option.LogicalHeadlessRule{
				Mode: C.LogicalTypeOr,
				Rules: []option.HeadlessRule{
					{
						Type: C.RuleTypeDefault,
						DefaultOptions: option.DefaultHeadlessRule{
							AdGuardDomain: importantDomain,
							DomainRegex:   importantDomainRegex,
							IPCIDR:        importantIPCIDR,
						},
					},
					currentRule,
				},
			},
		}
	}
	if len(importantExcludeDomain) > 0 || len(importantExcludeDomainRegex) > 0 || len(importantExcludeIPCIDR) > 0 {
		currentRule = option.HeadlessRule{
			Type: C.RuleTypeLogical,
			LogicalOptions: option.LogicalHeadlessRule{
				Mode: C.LogicalTypeAnd,
				Rules: []option.HeadlessRule{
					{
						Type: C.RuleTypeDefault,
						DefaultOptions: option.DefaultHeadlessRule{
							AdGuardDomain: importantExcludeDomain,
							DomainRegex:   importantExcludeDomainRegex,
							IPCIDR:        importantExcludeIPCIDR,
							Invert:        true,
						},
					},
					currentRule,
				},
			},
		}
	}
	if ignoredLines > 0 {
		logger.Info("parsed rules: ", len(ruleLines), "/", len(ruleLines)+ignoredLines)
	}
	return []option.HeadlessRule{currentRule}, nil
}

var ErrInvalid = E.New("invalid binary AdGuard rule-set")

func FromOptions(rules []option.HeadlessRule) ([]byte, error) {
	if len(rules) != 1 {
		return nil, ErrInvalid
	}
	rule := rules[0]
	var (
		importantDomain             []string
		importantDomainRegex        []string
		importantIPCIDR             []string
		importantExcludeDomain      []string
		importantExcludeDomainRegex []string
		importantExcludeIPCIDR      []string
		domain                      []string
		domainRegex                 []string
		ipCIDR                      []string
		excludeDomain               []string
		excludeDomainRegex          []string
		excludeIPCIDR               []string
	)
parse:
	for {
		switch rule.Type {
		case C.RuleTypeLogical:
			if !(len(rule.LogicalOptions.Rules) == 2 && rule.LogicalOptions.Rules[0].Type == C.RuleTypeDefault) {
				return nil, ErrInvalid
			}
			if rule.LogicalOptions.Mode == C.LogicalTypeAnd && rule.LogicalOptions.Rules[0].DefaultOptions.Invert {
				if len(importantExcludeDomain) == 0 && len(importantExcludeDomainRegex) == 0 && len(importantExcludeIPCIDR) == 0 {
					importantExcludeDomain = rule.LogicalOptions.Rules[0].DefaultOptions.AdGuardDomain
					importantExcludeDomainRegex = rule.LogicalOptions.Rules[0].DefaultOptions.DomainRegex
					importantExcludeIPCIDR = rule.LogicalOptions.Rules[0].DefaultOptions.IPCIDR
					if len(importantExcludeDomain)+len(importantExcludeDomainRegex)+len(importantExcludeIPCIDR) == 0 {
						return nil, ErrInvalid
					}
				} else {
					excludeDomain = rule.LogicalOptions.Rules[0].DefaultOptions.AdGuardDomain
					excludeDomainRegex = rule.LogicalOptions.Rules[0].DefaultOptions.DomainRegex
					excludeIPCIDR = rule.LogicalOptions.Rules[0].DefaultOptions.IPCIDR
					if len(excludeDomain)+len(excludeDomainRegex)+len(excludeIPCIDR) == 0 {
						return nil, ErrInvalid
					}
				}
			} else if rule.LogicalOptions.Mode == C.LogicalTypeOr && !rule.LogicalOptions.Rules[0].DefaultOptions.Invert {
				importantDomain = rule.LogicalOptions.Rules[0].DefaultOptions.AdGuardDomain
				importantDomainRegex = rule.LogicalOptions.Rules[0].DefaultOptions.DomainRegex
				importantIPCIDR = rule.LogicalOptions.Rules[0].DefaultOptions.IPCIDR
				if len(importantDomain)+len(importantDomainRegex)+len(importantIPCIDR) == 0 {
					return nil, ErrInvalid
				}
			} else {
				return nil, ErrInvalid
			}
			rule = rule.LogicalOptions.Rules[1]
		case C.RuleTypeDefault:
			domain = rule.DefaultOptions.AdGuardDomain
			domainRegex = rule.DefaultOptions.DomainRegex
			ipCIDR = rule.DefaultOptions.IPCIDR
			if len(domain)+len(domainRegex)+len(ipCIDR) == 0 {
				return nil, ErrInvalid
			}
			break parse
		}
	}
	var output bytes.Buffer
	for _, ruleLine := range importantDomain {
		output.WriteString(ruleLine)
		output.WriteString("$important\n")
	}
	for _, ruleLine := range importantDomainRegex {
		output.WriteString("/")
		output.WriteString(ruleLine)
		output.WriteString("/$important\n")
	}
	for _, ruleLine := range importantIPCIDR {
		output.WriteString(ruleLine)
		output.WriteString("$important\n")
	}
	for _, ruleLine := range importantExcludeDomain {
		output.WriteString("@@")
		output.WriteString(ruleLine)
		output.WriteString("$important\n")
	}
	for _, ruleLine := range importantExcludeDomainRegex {
		output.WriteString("@@/")
		output.WriteString(ruleLine)
		output.WriteString("/$important\n")
	}
	for _, ruleLine := range importantExcludeIPCIDR {
		output.WriteString("@@")
		output.WriteString(ruleLine)
		output.WriteString("$important\n")
	}
	for _, ruleLine := range domain {
		output.WriteString(ruleLine)
		output.WriteString("\n")
	}
	for _, ruleLine := range domainRegex {
		output.WriteString("/")
		output.WriteString(ruleLine)
		output.WriteString("/\n")
	}
	for _, ruleLine := range ipCIDR {
		output.WriteString(ruleLine)
		output.WriteString("\n")
	}
	for _, ruleLine := range excludeDomain {
		output.WriteString("@@")
		output.WriteString(ruleLine)
		output.WriteString("\n")
	}
	for _, ruleLine := range excludeDomainRegex {
		output.WriteString("@@/")
		output.WriteString(ruleLine)
		output.WriteString("/\n")
	}
	for _, ruleLine := range excludeIPCIDR {
		output.WriteString("@@")
		output.WriteString(ruleLine)
		output.WriteString("\n")
	}
	return output.Bytes(), nil
}

func ignoreIPCIDRRegexp(ruleLine string) bool {
	if strings.HasPrefix(ruleLine, "(http?:\\/\\/)") {
		ruleLine = ruleLine[12:]
	} else if strings.HasPrefix(ruleLine, "(https?:\\/\\/)") {
		ruleLine = ruleLine[13:]
	} else if strings.HasPrefix(ruleLine, "^") {
		ruleLine = ruleLine[1:]
	}
	return common.Error(strconv.ParseUint(common.SubstringBefore(ruleLine, "\\."), 10, 8)) == nil ||
		common.Error(strconv.ParseUint(common.SubstringBefore(ruleLine, "."), 10, 8)) == nil
}

func parseAdGuardHostLine(ruleLine string) (string, error) {
	before, after, ok := strings.Cut(ruleLine, " ")
	if !ok {
		return "", os.ErrInvalid
	}
	address, err := netip.ParseAddr(before)
	if err != nil {
		return "", err
	}
	if !address.IsUnspecified() {
		return "", nil
	}
	domain := after
	if !M.IsDomainName(domain) {
		return "", E.New("invalid domain name: ", domain)
	}
	return domain, nil
}

func parseADGuardIPCIDRLine(ruleLine string) (netip.Prefix, error) {
	var isPrefix bool
	if strings.HasSuffix(ruleLine, ".") {
		isPrefix = true
		ruleLine = ruleLine[:len(ruleLine)-1]
	}
	ruleStringParts := strings.Split(ruleLine, ".")
	if len(ruleStringParts) > 4 || len(ruleStringParts) < 4 && !isPrefix {
		return netip.Prefix{}, os.ErrInvalid
	}
	ruleParts := make([]uint8, 0, len(ruleStringParts))
	for _, part := range ruleStringParts {
		rulePart, err := strconv.ParseUint(part, 10, 8)
		if err != nil {
			return netip.Prefix{}, err
		}
		ruleParts = append(ruleParts, uint8(rulePart))
	}
	bitLen := len(ruleParts) * 8
	for len(ruleParts) < 4 {
		ruleParts = append(ruleParts, 0)
	}
	return netip.PrefixFrom(netip.AddrFrom4([4]byte(ruleParts)), bitLen), nil
}

