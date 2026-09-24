package clashapi

import (
	"net/http"

	"github.com/sagernet/sing-box/adapter"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

func dnsGroupRouter(manager adapter.DNSTransportManager) http.Handler {
	router := chi.NewRouter()
	router.Get("/", func(writer http.ResponseWriter, request *http.Request) {
		groups := make([]adapter.DNSGroupSnapshot, 0)
		if manager != nil {
			for _, transport := range manager.Transports() {
				if provider, loaded := transport.(adapter.DNSGroupSnapshotProvider); loaded {
					groups = append(groups, provider.GroupSnapshot())
				}
			}
		}
		render.JSON(writer, request, render.M{"groups": groups})
	})
	router.Get("/{tag}", func(writer http.ResponseWriter, request *http.Request) {
		tag := chi.URLParam(request, "tag")
		if manager != nil {
			if transport, loaded := manager.Transport(tag); loaded {
				if provider, isGroup := transport.(adapter.DNSGroupSnapshotProvider); isGroup {
					render.JSON(writer, request, provider.GroupSnapshot())
					return
				}
			}
		}
		render.Status(request, http.StatusNotFound)
		render.JSON(writer, request, newError("DNS group not found"))
	})
	return router
}
