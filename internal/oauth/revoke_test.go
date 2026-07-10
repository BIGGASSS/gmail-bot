package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/BIGGASSS/gmail-bot/internal/config"
)

func TestRevokeTokenAccepts200And400(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("token") != "refresh-token" {
					t.Fatalf("token=%q", r.URL.Query().Get("token"))
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			client := NewClient(config.Settings{
				GoogleClientID:     "id",
				GoogleClientSecret: "secret",
				AppBaseURL:         "https://example.com",
			}, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				cloned := req.Clone(req.Context())
				u, _ := url.Parse(server.URL)
				cloned.URL.Scheme = u.Scheme
				cloned.URL.Host = u.Host
				cloned.URL.Path = "/"
				return http.DefaultTransport.RoundTrip(cloned)
			})})

			if err := client.RevokeToken(context.Background(), "refresh-token"); err != nil {
				t.Fatalf("revoke: %v", err)
			}
		})
	}
}

func TestRevokeTokenRejectsOtherStatuses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(config.Settings{
		GoogleClientID:     "id",
		GoogleClientSecret: "secret",
		AppBaseURL:         "https://example.com",
	}, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		u, _ := url.Parse(server.URL)
		cloned.URL.Scheme = u.Scheme
		cloned.URL.Host = u.Host
		return http.DefaultTransport.RoundTrip(cloned)
	})})

	err := client.RevokeToken(context.Background(), "refresh-token")
	if err == nil {
		t.Fatal("expected error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
