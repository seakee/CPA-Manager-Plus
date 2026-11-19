package middleware

import (
	"net/http"
	"strings"
)

// adminFetchMetadataRejected reports whether a browser has labelled an admin
// request as coming from another site, for a destination that carries admin
// data rather than a page or a subresource.
//
// Manager authorizes admin calls with a request header, never a cookie, so this
// is defence in depth rather than the primary gate: it exists because a plugin
// page runs in an opaque-origin sandbox document whose own fetch/XHR calls the
// browser marks `Sec-Fetch-Site: cross-site`. Refusing that shape means a
// plugin cannot use the Console's admin API even if it somehow learns a
// credential, and it cannot use the API as a confused deputy.
//
// The rule is deliberately narrow so no leg of the admin UI breaks:
//   - only `cross-site` is refused; `same-origin`, `same-site`, `none` pass;
//   - a request without Sec-Fetch-Site is a non-browser client (CLI, probe,
//     automation) and passes, matching the transport's existing contract;
//   - only an `empty` destination (fetch/XHR) is refused, so a cross-site link
//     navigation to the Console, an <img>, a stylesheet or a script keeps its
//     normal behaviour instead of becoming a hard 403.
func adminFetchMetadataRejected(r *http.Request) bool {
	if r == nil {
		return false
	}
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site != "cross-site" {
		return false
	}
	destination := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest")))
	return destination == "" || destination == "empty"
}

// RejectCrossSiteAdmin wraps an admin surface and refuses cross-site
// fetch/XHR requests. exempt, when non-nil, marks requests that carry their own
// request-side isolation policy and must be judged there instead.
//
// The exemption takes the whole request, not just the path, because "is this a
// plugin resource" is a statement about the request shape: an encoded or
// dot-segment path that a canonical router rejects must not be exempted from
// the cross-site check either.
func RejectCrossSiteAdmin(next http.Handler, exempt func(*http.Request) bool) http.Handler {
	if next == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt != nil && exempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		if adminFetchMetadataRejected(r) {
			w.Header().Set("Cache-Control", "no-store")
			if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
				w.Header().Set("Connection", "close")
			}
			http.Error(w, "cross-site admin request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
