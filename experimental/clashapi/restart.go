package clashapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

func restartRouter(server *Server) http.Handler {
	r := chi.NewRouter()
	r.Post("/", restart(server))
	return r
}

func restart(server *Server) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			server.logger.Warn("sing-box restarting...")
			server.router.Reload()
		}()
		render.JSON(w, r, render.M{"status": "ok"})
	}
}
