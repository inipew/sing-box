package clashapi

import (
	"context"
	"net/http"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

type ruleSetRouter interface {
	RuleSet(tag string) (adapter.RuleSet, bool)
	RuleSets() []adapter.RuleSet
}

func ruleProviderRouter(router ruleSetRouter) http.Handler {
	r := chi.NewRouter()
	r.Get("/", getRuleProviders(router))

	r.Route("/{name}", func(r chi.Router) {
		r.Use(parseProviderName, findRuleProviderByName(router))
		r.Get("/", getRuleProvider)
		r.Put("/", updateRuleProvider)
	})
	return r
}

func ruleSetInfo(ruleSet adapter.RuleSetProvider) render.M {
	providerInfo := ruleSet.ProviderInfo()
	vehicleType := "File"
	if providerInfo.Type == C.RuleSetTypeRemote {
		vehicleType = "HTTP"
	}
	behavior := "Inline"
	switch providerInfo.Format {
	case C.RuleSetFormatSource:
		behavior = "Source"
	case C.RuleSetFormatBinary:
		behavior = "Binary"
	}
	return render.M{
		"name":        ruleSet.Name(),
		"type":        "Rule",
		"vehicleType": vehicleType,
		"behavior":    behavior,
		"ruleCount":   providerInfo.RuleCount,
		"updatedAt":   providerInfo.UpdatedAt.Format(time.RFC3339),
	}
}

func getRuleProviders(router ruleSetRouter) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		providers := make(map[string]render.M)
		for _, ruleSet := range router.RuleSets() {
			provider, isProvider := ruleSet.(adapter.RuleSetProvider)
			if isProvider {
				providers[provider.Name()] = ruleSetInfo(provider)
			}
		}
		render.JSON(writer, request, render.M{"providers": providers})
	}
}

func getRuleProvider(writer http.ResponseWriter, request *http.Request) {
	provider := request.Context().Value(CtxKeyProvider).(adapter.RuleSetProvider)
	render.JSON(writer, request, ruleSetInfo(provider))
}

func updateRuleProvider(writer http.ResponseWriter, request *http.Request) {
	provider := request.Context().Value(CtxKeyProvider).(adapter.RuleSetProvider)
	if err := provider.Update(request.Context()); err != nil {
		render.Status(request, http.StatusInternalServerError)
		render.JSON(writer, request, newError(err.Error()))
		return
	}
	render.NoContent(writer, request)
}

func findRuleProviderByName(router ruleSetRouter) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			name := request.Context().Value(CtxKeyProviderName).(string)
			ruleSet, loaded := router.RuleSet(name)
			if !loaded {
				render.Status(request, http.StatusNotFound)
				render.JSON(writer, request, ErrNotFound)
				return
			}
			provider, isProvider := ruleSet.(adapter.RuleSetProvider)
			if !isProvider {
				render.Status(request, http.StatusNotFound)
				render.JSON(writer, request, ErrNotFound)
				return
			}
			ctx := context.WithValue(request.Context(), CtxKeyProvider, provider)
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}
}
