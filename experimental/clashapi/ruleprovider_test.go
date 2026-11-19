package clashapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/stretchr/testify/require"
)

func TestRuleProviderListAndDetail(t *testing.T) {
	updatedAt := time.Date(2026, time.September, 22, 7, 0, 0, 0, time.UTC)
	provider := &ruleProviderTestSet{
		name: "remote-rules",
		info: adapter.RuleSetProviderInfo{
			Type:      C.RuleSetTypeRemote,
			Format:    C.RuleSetFormatBinary,
			RuleCount: 42,
			UpdatedAt: updatedAt,
		},
	}
	router := ruleProviderRouter(&ruleProviderTestRouter{sets: []adapter.RuleSet{provider}})

	for _, path := range []string{"/", "/remote-rules/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)

		require.Equal(t, http.StatusOK, response.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		if path == "/" {
			body = body["providers"].(map[string]any)["remote-rules"].(map[string]any)
		}
		require.Equal(t, "remote-rules", body["name"])
		require.Equal(t, "Rule", body["type"])
		require.Equal(t, "HTTP", body["vehicleType"])
		require.Equal(t, "Binary", body["behavior"])
		require.Equal(t, float64(42), body["ruleCount"])
		require.Equal(t, updatedAt.Format(time.RFC3339), body["updatedAt"])
	}
}

func TestRuleProviderUpdateReturnsProviderError(t *testing.T) {
	provider := &ruleProviderTestSet{name: "broken", updateErr: errors.New("download failed")}
	router := ruleProviderRouter(&ruleProviderTestRouter{sets: []adapter.RuleSet{provider}})
	request := httptest.NewRequest(http.MethodPut, "/broken/", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Contains(t, response.Body.String(), "download failed")
}

func TestRuleProviderUpdateSuccess(t *testing.T) {
	provider := &ruleProviderTestSet{name: "good"}
	router := ruleProviderRouter(&ruleProviderTestRouter{sets: []adapter.RuleSet{provider}})
	request := httptest.NewRequest(http.MethodPut, "/good/", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
}

func TestRuleProviderUpdateNotFound(t *testing.T) {
	router := ruleProviderRouter(&ruleProviderTestRouter{sets: []adapter.RuleSet{}})
	request := httptest.NewRequest(http.MethodPut, "/missing/", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusNotFound, response.Code)
}

func TestRuleProviderNonProviderRuleSetNotFound(t *testing.T) {
	plain := &nonProviderTestSet{name: "plain"}
	router := ruleProviderRouter(&ruleProviderTestRouter{sets: []adapter.RuleSet{plain}})
	request := httptest.NewRequest(http.MethodPut, "/plain/", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusNotFound, response.Code)
}

func TestRuleProviderUpdatePassesContext(t *testing.T) {
	type ctxKey struct{}
	var receivedCtx context.Context
	provider := &ruleProviderTestSet{
		name: "test-ctx",
		updateFn: func(ctx context.Context) error {
			receivedCtx = ctx
			return nil
		},
	}
	router := ruleProviderRouter(&ruleProviderTestRouter{sets: []adapter.RuleSet{provider}})
	request := httptest.NewRequest(http.MethodPut, "/test-ctx/", nil)
	ctx := context.WithValue(request.Context(), ctxKey{}, "val123")
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.NotNil(t, receivedCtx)
	require.Equal(t, "val123", receivedCtx.Value(ctxKey{}))
}

type nonProviderTestSet struct {
	adapter.RuleSet
	name string
}

func (s *nonProviderTestSet) Name() string {
	return s.name
}

type ruleProviderTestRouter struct {
	sets []adapter.RuleSet
}

func (r *ruleProviderTestRouter) RuleSet(tag string) (adapter.RuleSet, bool) {
	for _, ruleSet := range r.sets {
		if ruleSet.Name() == tag {
			return ruleSet, true
		}
	}
	return nil, false
}

func (r *ruleProviderTestRouter) RuleSets() []adapter.RuleSet {
	return r.sets
}

type ruleProviderTestSet struct {
	adapter.RuleSet
	name      string
	info      adapter.RuleSetProviderInfo
	updateErr error
	updateFn  func(ctx context.Context) error
}

func (s *ruleProviderTestSet) Name() string {
	return s.name
}

func (s *ruleProviderTestSet) ProviderInfo() adapter.RuleSetProviderInfo {
	return s.info
}

func (s *ruleProviderTestSet) Update(ctx context.Context) error {
	if s.updateFn != nil {
		return s.updateFn(ctx)
	}
	return s.updateErr
}
