package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// withChiURLParam injects a chi URL parameter onto the request context so
// handler tests can use chi.URLParam without going through the router.
func withChiURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// TestHandleAddWishlist_Validation is a fuzz-discovered regression test: the
// previous handler accepted oversized titles, wrong/missing Content-Type, and
// non-numeric IDs on DELETE. These cases now error out with the right status
// instead of silently succeeding.
//
// We exercise the wrapping behavior here (Content-Type gating, decoder error
// surface, length cap path). End-to-end success requires a Manager — that
// path is covered by the existing handler tests.
func TestAddWishlist_ContentTypeGating(t *testing.T) {
	cases := []struct {
		name string
		ct   string
		body string
		want int
	}{
		{"text/plain rejected", "text/plain", `{"title":"x"}`, 415},
		{"application/xml rejected", "application/xml", `{"title":"x"}`, 415},
		{"application/json with garbage body errors as 400", "application/json", "{not json", 400},
		{"application/json; charset=utf-8 accepted (parses, hits no-Manager path)", "application/json; charset=utf-8", `{"title":""}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/wishlist", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.ct)
			rr := httptest.NewRecorder()
			// We invoke the function but it requires a Manager to fully succeed;
			// these are pre-Manager validation gates, so they short-circuit before
			// the Manager is touched.
			s := &Server{}
			s.handleAddWishlist(rr, req)
			if rr.Code != tc.want {
				t.Errorf("Content-Type=%q body=%q: got %d, want %d (body: %s)", tc.ct, tc.body, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestAddWishlist_LengthCaps(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"oversize title (5KB)", `{"title":"` + strings.Repeat("A", 5000) + `","platform":"NES","platform_slug":"nes"}`, 400},
		{"oversize platform_slug", `{"title":"OK","platform":"NES","platform_slug":"` + strings.Repeat("x", 100) + `"}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/wishlist", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s := &Server{}
			s.handleAddWishlist(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d (body: %s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestDeleteWishlist_InvalidIDs(t *testing.T) {
	// Non-numeric IDs previously returned HTTP 200; now should be 400.
	// We exercise the validation path directly via httptest. We don't go
	// through the chi router here because that requires the route table
	// setup; instead we set the chi URL param manually.
	cases := []struct {
		name string
		id   string
		want int
	}{
		{"garbage non-numeric", "garbage", 400},
		{"empty", "", 400},
		{"zero (out of bounds)", "0", 400},
		{"negative", "-1", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("DELETE", "/api/wishlist/"+tc.id, nil)
			req = withChiURLParam(req, "id", tc.id)
			rr := httptest.NewRecorder()
			s := &Server{}
			s.handleDeleteWishlist(rr, req)
			if rr.Code != tc.want {
				t.Errorf("id=%q: got %d, want %d (body: %s)", tc.id, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}
