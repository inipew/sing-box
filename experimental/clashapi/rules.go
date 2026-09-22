package clashapi

import (
	"net/http"

	"github.com/sagernet/sing-box/adapter"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

type ruleLister interface {
	Rules() []adapter.Rule
}

func ruleRouter(router ruleLister, dnsRouter adapter.DNSRuleInfoProvider) http.Handler {
	r := chi.NewRouter()
	r.Get("/", getRules(router, dnsRouter))
	return r
}

type Rule struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Proxy   string `json:"proxy"`
}

func getRules(router ruleLister, dnsRouter adapter.DNSRuleInfoProvider) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var rules []Rule
		if dnsRouter != nil {
			for _, rule := range dnsRouter.DNSRuleInfo() {
				rules = append(rules, Rule{
					Type:    rule.Type,
					Payload: rule.Payload,
					Proxy:   rule.Action,
				})
			}
		}
		for _, rule := range router.Rules() {
			rules = append(rules, Rule{
				Type:    rule.Type(),
				Payload: rule.String(),
				Proxy:   rule.Action().String(),
			})
		}
		render.JSON(w, r, render.M{
			"rules": rules,
		})
	}
}
