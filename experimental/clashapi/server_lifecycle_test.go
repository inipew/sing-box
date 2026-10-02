package clashapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/service"
	"github.com/stretchr/testify/require"
)

type uiTestTransport struct{ http.RoundTripper }

func (*uiTestTransport) CloseIdleConnections() {}
func (*uiTestTransport) Reset()                {}

type uiTestHTTPClientManager struct {
	adapter.HTTPClientManager
	transport adapter.HTTPTransport
}

func (m *uiTestHTTPClientManager) DefaultTransport() adapter.HTTPTransport { return m.transport }

func newUIUpdateTestServer(t *testing.T, downloadURL string) (*Server, *adapter.Scope, string) {
	t.Helper()
	ctx := service.ContextWith[adapter.HTTPClientManager](context.Background(), &uiTestHTTPClientManager{transport: &uiTestTransport{RoundTripper: http.DefaultTransport}})
	logger := log.NewNOPFactory().Logger()
	ui := filepath.Join(t.TempDir(), "ui")
	server := &Server{
		ctx: ctx, logger: logger,
		httpServer:               &http.Server{Addr: "127.0.0.1:0", Handler: http.NewServeMux()},
		externalController:       true,
		externalUI:               ui,
		externalUIDownloadURL:    downloadURL,
		externalUIUpdateInterval: 20 * time.Millisecond,
	}
	scope := adapter.NewScope(ctx, logger)
	t.Cleanup(func() { require.NoError(t, scope.Close()) })
	return server, scope, ui
}

func TestExternalUIInitialDownloadFailureIsRetried(t *testing.T) {
	archive := createExternalUIArchive(t, map[string]string{"ui/index.html": "installed"})
	var attempts atomic.Int32
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer download.Close()
	server, scope, ui := newUIUpdateTestServer(t, download.URL)
	require.NoError(t, server.Start(adapter.StartStateStarted, scope))

	require.Eventually(t, func() bool {
		content, err := os.ReadFile(filepath.Join(ui, "index.html"))
		return err == nil && string(content) == "installed"
	}, time.Second, 10*time.Millisecond)
	require.GreaterOrEqual(t, attempts.Load(), int32(2))
}

func TestExternalUIUpdaterStopsWhenScopeCloses(t *testing.T) {
	archive := createExternalUIArchive(t, map[string]string{"ui/index.html": "installed"})
	var attempts atomic.Int32
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		_, _ = w.Write(archive)
	}))
	defer download.Close()
	server, scope, _ := newUIUpdateTestServer(t, download.URL)
	require.NoError(t, server.Start(adapter.StartStateStarted, scope))
	require.NotNil(t, server.ticker)
	require.NoError(t, scope.Close())
	before := attempts.Load()
	// Restart the stopped timer to expose a stray updater after closing its scope.
	server.ticker.Reset(5 * time.Millisecond)
	defer server.ticker.Stop()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, before, attempts.Load())
}

func TestExternalUIActiveUpdateIsCanceledWhenScopeCloses(t *testing.T) {
	archive := createExternalUIArchive(t, map[string]string{"ui/index.html": "installed"})
	var attempts atomic.Int32
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	release := make(chan struct{})
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			_, _ = w.Write(archive)
			return
		}
		close(requestStarted)
		select {
		case <-r.Context().Done():
			close(requestCanceled)
		case <-release:
		}
	}))
	defer download.Close()
	defer close(release)
	server, scope, _ := newUIUpdateTestServer(t, download.URL)
	require.NoError(t, server.Start(adapter.StartStateStarted, scope))
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("updater did not start a request")
	}
	require.NoError(t, scope.Close())
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("active UI update continued after scope close")
	}
}

type uiTestPlatform struct{ adapter.PlatformInterface }

func TestPlatformConfigReloadIsUnavailable(t *testing.T) {
	ctx := service.ContextWith[adapter.PlatformInterface](context.Background(), &uiTestPlatform{})
	server := &Server{ctx: ctx}
	request := httptest.NewRequest(http.MethodPut, "/", nil)
	response := httptest.NewRecorder()
	configRouter(server, log.NewNOPFactory()).ServeHTTP(response, request)
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
}
