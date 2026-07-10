package oauth

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/BIGGASSS/gmail-bot/internal/config"
)

func TestDecodeJSONResponseExposesInvalidGrantDetails(t *testing.T) {
	_ = config.Settings{} // keep dependency explicit for package docs
	resp := &http.Response{
		StatusCode: 400,
		Body: io.NopCloser(strings.NewReader(`{
			"error": "invalid_grant",
			"error_description": "Token has been expired or revoked."
		}`)),
		Header: make(http.Header),
	}
	_, err := DecodeJSONResponseForTest(resp)
	if err == nil {
		t.Fatal("expected oauth error")
	}
	oauthErr, ok := err.(*OAuthError)
	if !ok {
		t.Fatalf("expected *OAuthError, got %T", err)
	}
	if oauthErr.StatusCode != 400 {
		t.Fatalf("status=%d", oauthErr.StatusCode)
	}
	if oauthErr.ErrorCode != "invalid_grant" {
		t.Fatalf("error=%q", oauthErr.ErrorCode)
	}
	if oauthErr.ErrorDescription != "Token has been expired or revoked." {
		t.Fatalf("description=%q", oauthErr.ErrorDescription)
	}
	if !oauthErr.IsInvalidGrant() {
		t.Fatal("expected invalid grant")
	}
}
