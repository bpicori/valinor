package factory

import (
	"net"
	"net/http"
	"time"
)

// RequestTimeout is the max duration to establish a connection and receive response headers.
const RequestTimeout = 60 * time.Second

func httpClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: RequestTimeout,
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	transport.TLSHandshakeTimeout = RequestTimeout
	transport.ResponseHeaderTimeout = RequestTimeout

	return &http.Client{
		Transport: transport,
	}
}