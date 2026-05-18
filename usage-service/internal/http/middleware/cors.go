package middleware

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/config"
)

func WithCORS(cfg config.Config, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteCORS(cfg, w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func WriteCORS(cfg config.Config, w http.ResponseWriter, r *http.Request) {
	if len(cfg.CORSOrigins) == 0 {
		return
	}
	origin := r.Header.Get("Origin")
	allowed := cfg.CORSOrigins[0]
	for _, candidate := range cfg.CORSOrigins {
		if candidate == "*" || candidate == origin {
			allowed = candidate
			break
		}
	}
	if allowed == "*" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else if origin != "" && allowed == origin {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
}
