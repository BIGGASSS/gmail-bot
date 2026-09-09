package oauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/BIGGASSS/gmail-bot/internal/config"
)

func TestOAuthErrorsDoNotEchoResponseBody(t *testing.T) {
	client := NewClient(config.Settings{}, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("upstream echoed SECRET")), Header: make(http.Header)}, nil
	})})
	_, exchangeErr := client.ExchangeCode(context.Background(), "SECRET")
	revokeErr := client.RevokeToken(context.Background(), "SECRET")
	for _, err := range []error{exchangeErr, revokeErr} {
		if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "500") {
			t.Fatalf("unsafe or unhelpful error: %v", err)
		}
	}
}
