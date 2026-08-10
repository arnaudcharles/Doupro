// Package tlsconfig configures additive trust for outbound HTTPS clients.
package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
)

// ConfigureDefaultTransport adds the PEM certificates at caCertPath to the
// system trust pool used by net/http's default transport. This affects
// notification providers such as ntfy as well as registry/webhook clients
// that use http.DefaultClient. Public system roots remain trusted and TLS
// verification is never disabled.
func ConfigureDefaultTransport(caCertPath string) error {
	if caCertPath == "" {
		return nil
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return fmt.Errorf("default HTTP transport has unsupported type %T", http.DefaultTransport)
	}
	transport, err := transportWithExtraCA(base, caCertPath)
	if err != nil {
		return err
	}
	http.DefaultTransport = transport
	return nil
}

func transportWithExtraCA(base *http.Transport, caCertPath string) (*http.Transport, error) {
	pem, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read DOUPRO_EXTRA_CA_CERT_PATH (%s): %w", caCertPath, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if ok := pool.AppendCertsFromPEM(pem); !ok {
		return nil, fmt.Errorf("no valid PEM certificate found in DOUPRO_EXTRA_CA_CERT_PATH (%s)", caCertPath)
	}

	transport := base.Clone()
	tlsConfig := &tls.Config{RootCAs: pool}
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
		tlsConfig.RootCAs = pool
	}
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}
