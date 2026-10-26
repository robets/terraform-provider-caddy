package caddyapi

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"

	"github.com/go-resty/resty/v2"
)

// URLFromID Converts an ID into it's resource URL
// "@config/apps/http" -> "/config/apps/http"
// "my_server/routes/0" -> "/id/my_server/routes/0"
func URLFromID(id string) string {
	if strings.HasPrefix(id, "@config") {
		return "/" + id[1:]
	}
	return "/id/" + id
}

// Client represents a Caddy API Client
type Client struct {
	client *resty.Client
}

type DialFunc = func(network, addr string) (net.Conn, error)

func NewClient(host string, dial DialFunc) *Client {
	return &Client{
		client: resty.New().SetTransport(&http.Transport{Dial: dial}).SetHostURL(host),
	}
}

func NewUnixClient(socket string, dial DialFunc) *Client {
	if dial == nil {
		dial = net.Dial
	}
	transport := http.Transport{
		Dial: func(_, _ string) (net.Conn, error) {
			return dial("unix", socket)
		},
	}
	return &Client{
		client: resty.New().SetTransport(&transport).SetScheme("http").SetHostURL(""),
	}
}

// SetBearerToken configures authentication on every request, including snapshot
// reads and conditional mutation retries. Call before using the client.
func (c *Client) SetBearerToken(token string) error {
	for i := 0; i < len(token); i++ {
		if token[i] <= ' ' || token[i] >= 0x7f {
			return fmt.Errorf("bearer_token must contain only visible ASCII characters without whitespace")
		}
	}
	if token == "" {
		return nil
	}
	c.client.SetHeader("Authorization", "Bearer "+token)
	// net/http's default policy can retain Authorization across different
	// ports and subdomains. Refuse any origin change instead, including HTTPS
	// downgrades. The header is set before redirects, never by a transport hook.
	c.client.GetClient().CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errRedirectLimit
		}
		origin := via[0].URL
		if !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
			return errRedirectOrigin
		}
		return nil
	}
	return nil
}

var (
	errRedirectOrigin = errors.New("refusing authenticated redirect to a different origin")
	errRedirectLimit  = errors.New("stopped after 10 redirects")
)

// safeError is the boundary for request and response-decoding errors. With
// authentication enabled, errors may contain server-controlled URLs or text
// that echoes the credential. Report only known classifications, not raw text.
// Unauthenticated clients retain their existing diagnostics.
func (c *Client) safeError(err error) error {
	if err == nil || c.client.Header.Get("Authorization") == "" {
		return err
	}
	switch {
	case errors.Is(err, errRedirectOrigin):
		return errRedirectOrigin
	case errors.Is(err, errRedirectLimit):
		return errRedirectLimit
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	var unknownCA x509.UnknownAuthorityError
	if errors.As(err, &unknownCA) {
		return errors.New("TLS certificate signed by unknown authority")
	}
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	if errors.As(err, &hostname) || errors.As(err, &invalidCertificate) {
		return errors.New("TLS certificate verification failed")
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.New("authenticated API request timed out")
	}
	return errors.New("authenticated API request failed (error details omitted)")
}

// EnforceExists ensures that the path exists. If it doesn't currently exist, it sets it to be empty
func (c *Client) EnforceExists(id string) error {
	split := strings.Split(id, "/")
	for i := len(split); i > 1; i-- {
		resp, err := c.client.R().Get(URLFromID(path.Join(split[:i]...)))
		if err != nil {
			return c.safeError(err)
		}
		if resp.IsError() {
			continue
		}

		body := resp.Body()
		if string(body) == "null" {
			data := makeEmptyObject(split[i:])
			resp, err = c.client.R().SetBody(data).Post(URLFromID(path.Join(split[:i]...)))
			if err != nil {
				return c.safeError(err)
			}
			if resp.IsError() {
				return StatusError{resp}
			}
		}

		return nil
	}
	return nil
}

func makeEmptyObject(split []string) map[string]interface{} {
	data := make(map[string]interface{})
	if len(split) != 0 {
		data[split[0]] = makeEmptyObject(split[1:])
	}
	return data
}

// StatusError is the error returned when Request responds with non-2XX
type StatusError struct {
	resp *resty.Response
}

func (s StatusError) Error() string {
	// Authenticated endpoints can echo credentials in status reason phrases,
	// bodies, or redirect-derived URLs. Only format trusted status text here.
	if s.resp.Request.Header.Get("Authorization") != "" {
		return fmt.Sprintf("%s: HTTP %d %s", s.resp.Request.Method, s.resp.StatusCode(), http.StatusText(s.resp.StatusCode()))
	}
	str := fmt.Sprintf("%s %s: %d %s", s.resp.Request.Method, s.resp.Request.URL, s.resp.StatusCode(), s.resp.Status())
	if body := s.resp.Body(); len(body) > 0 {
		return fmt.Sprintf("%s - body: [%s]", str, string(body))
	}
	return str
}
