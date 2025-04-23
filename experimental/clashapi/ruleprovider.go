package clashapi

import (
	"context"
	"net/http"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/json/badjson"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

func ruleProviderRouter(router adapter.Router) http.Handler {
	r := chi.NewRouter()
	r.Get("/", getRuleProviders(router))

	r.Route("/{name}", func(r chi.Router) {
		r.Use(parseProviderName, findRuleProviderByName(router))
		r.Get("/", getRuleProvider)
		r.Put("/", updateRuleProvider)
	})
	return r
}

func ruleSetInfo(ruleSet adapter.RuleSet) *badjson.JSONObject {
	var info badjson.JSONObject
	info.Put("name", ruleSet.Name())
	info.Put("type", "Rule")

	var vehicleType string
	switch ruleSet.Type() {
	case C.RuleSetTypeRemote:
		vehicleType = "HTTP"
	default:
		vehicleType = "File"
	}
	info.Put("vehicleType", vehicleType)
	info.Put("behavior", C.RuleSetFormatDisplayName(ruleSet.Format()))
	info.Put("ruleCount", ruleSet.RuleCount())
	info.Put("updatedAt", ruleSet.UpdatedAt().Format(time.RFC3339))
	return &info
}

func getRuleProviders(router adapter.Router) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var providerMap badjson.JSONObject
		for i, ruleSet := range router.RuleSets() {
			var tag string
			if ruleSet.Name() == "" {
				tag = F.ToString(i)
			} else {
				tag = ruleSet.Name()
			}
			providerMap.Put(tag, ruleSetInfo(ruleSet))
		}

		var responseMap badjson.JSONObject
		responseMap.Put("providers", &providerMap)
		response, err := responseMap.MarshalJSON()
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, newError(err.Error()))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(response)
	}
}

func getRuleProvider(w http.ResponseWriter, r *http.Request) {
	ruleSet := r.Context().Value(CtxKeyProvider).(adapter.RuleSet)
	response, err := ruleSetInfo(ruleSet).MarshalJSON()
	if err != nil {
		render.Status(r, http.StatusInternalServerError)
		render.JSON(w, r, newError(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(response)
}

func updateRuleProvider(w http.ResponseWriter, r *http.Request) {
	ruleSet := r.Context().Value(CtxKeyProvider).(adapter.RuleSet)
	if err := ruleSet.Update(r.Context()); err != nil {
		render.Status(r, http.StatusInternalServerError)
		render.JSON(w, r, newError(err.Error()))
		return
	}
	render.NoContent(w, r)
}

func findRuleProviderByName(router adapter.Router) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Context().Value(CtxKeyProviderName).(string)
			provider, exist := router.RuleSet(name)
			if !exist {
				render.Status(r, http.StatusNotFound)
				render.JSON(w, r, ErrNotFound)
				return
			}
			ctx := context.WithValue(r.Context(), CtxKeyProvider, provider)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
