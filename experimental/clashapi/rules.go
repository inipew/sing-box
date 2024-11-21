package clashapi

import (
	"net/http"

	"github.com/sagernet/sing-box/adapter"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

func ruleRouter(router adapter.Router) http.Handler {
	r := chi.NewRouter()
	r.Get("/", getRules(router))
	return r
}

type Rule struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Proxy   string `json:"proxy"`
}

func getRules(router adapter.Router) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// rawRules := router.Rules()

		var rules []Rule
		// for _, rule := range rawRules {
		// 	rules = append(rules, Rule{
		// 		Type:    rule.Type(),
		// 		Payload: rule.String(),
		// 		Proxy:   rule.Action().String(),
		// 	})
		// }
		dnsRules := router.DNSRules()
		for _, rule := range dnsRules {
			rules = append(rules, Rule{
				Type:     "DNS",
				Payload:  rule.String(),
				Proxy:    rule.Action().String(),
			})
		}

		rules = append(rules, Rule{
			Type:    "DNS",
			Payload: "final",
			Proxy:   router.DefaultDNSServer(),
		})
		routeRules := router.Rules()
		for _, rule := range routeRules {
			rules = append(rules, Rule{
				Type:     "ROUTE",
				Payload:  rule.String(),
				Proxy:    rule.Action().String(),
			})
		}
		// finalRules := []Rule{}
		// finalTCPOut, _ := router.DefaultOutbound(N.NetworkTCP)
		// finalTCPTag := finalTCPOut.Tag()
		// if finalUDPOut, _ := router.DefaultOutbound(N.NetworkUDP); finalTCPOut == finalUDPOut {
		// 	finalRules = append(finalRules, Rule{
		// 		Type:    "ROUTE",
		// 		Payload: "final",
		// 		Proxy:   finalTCPTag,
		// 	})
		// } else {
		// 	finalUDPTag := finalUDPOut.Tag()
		// 	finalRules = append(finalRules, Rule{
		// 		Type:    "ROUTE",
		// 		Payload: "final_tcp",
		// 		Proxy:   finalTCPTag,
		// 	})
		// 	finalRules = append(finalRules, Rule{
		// 		Type:    "ROUTE",
		// 		Payload: "final_udp",
		// 		Proxy:   finalUDPTag,
		// 	})
		// }
		// rules = append(rules, finalRules...)
		render.JSON(w, r, render.M{
			"rules": rules,
		})
	}
}
