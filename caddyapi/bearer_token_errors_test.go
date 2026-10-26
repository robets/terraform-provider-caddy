package caddyapi

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBearerTokenErrorClassifications(t *testing.T) {
	const token = "synthetic-classification-probe"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"origin", &url.Error{Op: "Get", URL: "http://example.invalid/" + token, Err: errRedirectOrigin}, "different origin"},
		{"redirect limit", &url.Error{Op: "Get", URL: token, Err: errRedirectLimit}, "10 redirects"},
		{"cancellation", fmt.Errorf("%s: %w", token, context.Canceled), "context canceled"},
		{"deadline", fmt.Errorf("%s: %w", token, context.DeadlineExceeded), "deadline exceeded"},
		{"timeout", &net.DNSError{Name: token, IsTimeout: true}, "timed out"},
		{"certificate name", x509.HostnameError{Host: token}, "TLS certificate verification failed"},
		{"certificate validity", x509.CertificateInvalidError{Detail: token}, "TLS certificate verification failed"},
		{"untrusted CA", x509.UnknownAuthorityError{}, "certificate signed by unknown authority"},
		{"other error", errors.New(token), "error details omitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient("http://localhost", nil)
			require.True(t, tc.err == c.safeError(tc.err), "unauthenticated error changed")
			require.NoError(t, c.SetBearerToken(token))
			err := c.safeError(tc.err)
			require.Error(t, err)
			require.False(t, strings.Contains(err.Error(), token), "credential appeared in classification")
			require.Contains(t, err.Error(), tc.want)
			require.NoError(t, c.safeError(nil))
		})
	}
}
