package arcana

import (
	"net/http"
)

// authMiddleware wraps an http.Handler with identity extraction.
func authMiddleware(authFunc AuthFunc, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authFunc == nil {
			next.ServeHTTP(w, r)
			return
		}

		identity, err := authFunc(r)
		if err != nil {
			writeJSON(w, http.StatusConflict, errorResponse{
				OK:    false,
				Error: "unauthorized",
			})
			return
		}

		if identity == nil {
			writeJSON(w, http.StatusConflict, errorResponse{
				OK:    false,
				Error: "unauthorized",
			})
			return
		}

		ctx := WithIdentity(r.Context(), identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
