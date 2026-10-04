// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dao

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/port"
	gwebsocket "github.com/gorilla/websocket"
	"golang.org/x/net/proxy"
	"k8s.io/apimachinery/pkg/util/httpstream"
	streamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	constants "k8s.io/apimachinery/pkg/util/portforward"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport"
	clientspdy "k8s.io/client-go/transport/spdy"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

const (
	forwardHTTPScheme  = "http"
	forwardHTTPSScheme = "https"
)

// Captured upgrades keep an actual socket deadline and cancellation owner.
// Neither client-go's SPDY response reader nor Gorilla's upgrade response reader
// interrupts a blocked read merely because its request context was canceled.
func (p *PortForwarder) forwardCapturedTransport(setup, lifetime context.Context, cfg *rest.Config, endpoint *url.URL,
	tunnel port.PortTunnel, getAllowed, createAllowed bool,
) (*portforward.PortForwarder, error) {
	websockets := getAllowed && !cmdutil.PortForwardWebsockets.IsDisabled()
	if !websockets && !createAllowed {
		return nil, fmt.Errorf("%w: websocket endpoint access is required with get-only permission", ErrForwardEndpointDenied)
	}
	conf, err := cfg.TransportConfig()
	if err != nil {
		return nil, err
	}
	if conf.Transport != nil {
		return nil, fmt.Errorf("captured custom HTTP transport cannot safely provide an owned streaming socket")
	}
	tlsConfig, err := transport.TLSConfigFor(conf)
	if err != nil {
		return nil, err
	}
	if tlsConfig == nil {
		// An explicit secure default is needed by SPDY's UpgradeTransport path.
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	dial := (&net.Dialer{}).DialContext
	if conf.DialHolder != nil {
		dial = conf.DialHolder.Dial
	}
	proxier := conf.Proxy
	if proxier == nil {
		proxier = utilnet.NewProxierWithNoProxyCIDR(http.ProxyFromEnvironment)
	}
	dialer := &capturedForwardDialer{setup: setup, lifetime: lifetime, conf: conf, tls: tlsConfig,
		endpoint: *endpoint, sockets: &forwardSocketBook{dial: dial}, proxier: proxier, websocket: websockets, spdy: createAllowed}
	return portforward.NewOnAddresses(dialer, []string{tunnel.Address}, []string{tunnel.PortMap()}, p.stopChan, p.readyChan, p.Out, p.ErrOut)
}

type capturedForwardDialer struct {
	setup, lifetime context.Context
	conf            *transport.Config
	tls             *tls.Config
	endpoint        url.URL
	sockets         *forwardSocketBook
	proxier         func(*http.Request) (*url.URL, error)
	websocket, spdy bool
}

func (d *capturedForwardDialer) Dial(protocols ...string) (httpstream.Connection, string, error) {
	ctx, cancel := context.WithTimeout(d.setup, defaultTimeout)
	stopLifetime := context.AfterFunc(d.lifetime, cancel)
	defer stopLifetime()
	defer cancel()
	var connection httpstream.Connection
	var protocol string
	var err error
	if d.websocket {
		connection, protocol, err = d.dialWebsocket(ctx, protocols)
	}
	if d.spdy && (!d.websocket || httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)) {
		connection, protocol, err = d.dialSPDY(ctx, protocols)
	}
	if err != nil || ctx.Err() != nil {
		d.sockets.closeAll()
		return nil, "", errors.Join(err, ctx.Err())
	}
	if err = d.sockets.promote(ctx, d.lifetime); err != nil {
		_ = connection.Close()
		return nil, "", err
	}
	return connection, protocol, nil
}

func (d *capturedForwardDialer) dialSPDY(ctx context.Context, protocols []string) (httpstream.Connection, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint.String(), http.NoBody)
	if err != nil {
		return nil, "", err
	}
	upgrader := &capturedSPDYUpgrade{sockets: d.sockets, tls: d.tls, proxier: d.proxier}
	roundTripper, err := transport.HTTPWrappersForConfig(d.conf, upgrader)
	if err != nil {
		return nil, "", err
	}
	// Timeout on http.Client would return while the raw upgrade reader remains
	// blocked. The owned socket, instead, closes synchronously on cancellation.
	return clientspdy.Negotiate(upgrader, &http.Client{Transport: roundTripper}, request, protocols...)
}

type capturedSPDYUpgrade struct {
	sockets  *forwardSocketBook
	tls      *tls.Config
	proxier  func(*http.Request) (*url.URL, error)
	upgrader *streamspdy.SpdyRoundTripper
}

func (u *capturedSPDYUpgrade) RoundTrip(request *http.Request) (*http.Response, error) {
	// Route the same authenticated request seen by the captured wrappers.
	proxyURL, err := u.proxier(request)
	if err != nil {
		return nil, err
	}
	upgradeTransport := &http.Transport{TLSClientConfig: u.tls, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return u.sockets.dialEndpoint(ctx, network, address, proxyURL, u.tls)
	}}
	u.upgrader, err = streamspdy.NewRoundTripperWithConfig(streamspdy.RoundTripperConfig{UpgradeTransport: upgradeTransport, PingPeriod: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	return u.upgrader.RoundTrip(request)
}

func (u *capturedSPDYUpgrade) NewConnection(response *http.Response) (httpstream.Connection, error) {
	return u.upgrader.NewConnection(response)
}

func (d *capturedForwardDialer) dialWebsocket(ctx context.Context, protocols []string) (httpstream.Connection, string, error) {
	tunnelingProtocols := make([]string, len(protocols))
	for i, protocol := range protocols {
		tunnelingProtocols[i] = constants.WebsocketsSPDYTunnelingPrefix + protocol
	}
	upgrade := &capturedWebsocketUpgrade{dialer: gwebsocket.Dialer{TLSClientConfig: d.tls, Proxy: d.proxier,
		NetDialContext: d.sockets.open, Subprotocols: tunnelingProtocols, ReadBufferSize: 33 * 1024, WriteBufferSize: 33 * 1024}}
	roundTripper, err := transport.HTTPWrappersForConfig(d.conf, upgrade)
	if err != nil {
		return nil, "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, d.endpoint.String(), http.NoBody)
	if err != nil {
		return nil, "", err
	}
	response, err := roundTripper.RoundTrip(request)
	if err != nil {
		return nil, "", err
	}
	if err = response.Body.Close(); err != nil {
		_ = upgrade.connection.Close()
		return nil, "", err
	}
	connection, err := streamspdy.NewClientConnectionWithPings(portforward.NewTunnelingConnection("k9plus-port-forward", upgrade.connection), portforward.PingPeriod)
	if err != nil {
		_ = upgrade.connection.Close()
		return nil, "", err
	}
	return connection, strings.TrimPrefix(upgrade.connection.Subprotocol(), constants.WebsocketsSPDYTunnelingPrefix), nil
}

type capturedWebsocketUpgrade struct {
	dialer     gwebsocket.Dialer
	connection *gwebsocket.Conn
}

func (u *capturedWebsocketUpgrade) RoundTrip(request *http.Request) (*http.Response, error) {
	endpoint := *request.URL
	switch endpoint.Scheme {
	case forwardHTTPSScheme:
		endpoint.Scheme = "wss"
	case forwardHTTPScheme:
		endpoint.Scheme = "ws"
	default:
		return nil, fmt.Errorf("unsupported websocket endpoint scheme")
	}
	connection, response, err := u.dialer.DialContext(request.Context(), endpoint.String(), request.Header)
	if response != nil && err != nil {
		_ = response.Body.Close()
	}
	if errors.Is(err, gwebsocket.ErrBadHandshake) {
		return nil, &httpstream.UpgradeFailureError{Cause: err}
	}
	if err != nil {
		return nil, err
	}
	if !slices.Contains(u.dialer.Subprotocols, connection.Subprotocol()) {
		_ = connection.Close()
		_ = response.Body.Close()
		return nil, &httpstream.UpgradeFailureError{Cause: errors.New("server did not negotiate a supported forwarding protocol")}
	}
	u.connection = connection
	return response, nil
}

// Each socket is removed on Close. Failed fallback attempts cannot linger in
// the book or leave cancellation callbacks attached to dead connections.
type forwardSocketBook struct {
	mx      sync.Mutex
	dial    func(context.Context, string, string) (net.Conn, error)
	sockets map[*forwardSocket]struct{}
}

type forwardSocket struct {
	net.Conn
	book *forwardSocketBook
	stop func() bool
	once sync.Once
	err  error
}

func (b *forwardSocketBook) open(ctx context.Context, network, address string) (net.Conn, error) {
	connection, err := b.dial(ctx, network, address)
	if err != nil {
		return nil, err
	}
	socket := &forwardSocket{Conn: connection, book: b}
	if deadline, ok := ctx.Deadline(); ok {
		if err = connection.SetDeadline(deadline); err != nil {
			_ = connection.Close()
			return nil, err
		}
	}
	b.mx.Lock()
	if b.sockets == nil {
		b.sockets = make(map[*forwardSocket]struct{})
	}
	b.sockets[socket] = struct{}{}
	socket.stop = context.AfterFunc(ctx, func() { _ = socket.Close() })
	b.mx.Unlock()
	if err = ctx.Err(); err != nil {
		_ = socket.Close()
		return nil, err
	}
	return socket, nil
}

func (s *forwardSocket) Close() error {
	s.once.Do(func() {
		s.book.mx.Lock()
		delete(s.book.sockets, s)
		if s.stop != nil {
			s.stop()
		}
		s.book.mx.Unlock()
		s.err = s.Conn.Close()
	})
	return s.err
}

func (b *forwardSocketBook) closeAll() {
	b.mx.Lock()
	sockets := make([]*forwardSocket, 0, len(b.sockets))
	for socket := range b.sockets {
		sockets = append(sockets, socket)
	}
	b.mx.Unlock()
	for _, socket := range sockets {
		_ = socket.Close()
	}
}

func (b *forwardSocketBook) promote(setup, lifetime context.Context) error {
	b.mx.Lock()
	var err error
	for socket := range b.sockets {
		if !socket.stop() || setup.Err() != nil || lifetime.Err() != nil {
			err = errors.Join(setup.Err(), lifetime.Err(), context.Canceled)
			break
		}
		if err = socket.SetDeadline(time.Time{}); err != nil {
			break
		}
		socket.stop = context.AfterFunc(lifetime, func() { _ = socket.Close() })
	}
	b.mx.Unlock()
	if err != nil {
		b.closeAll()
	}
	return err
}

func (b *forwardSocketBook) dialEndpoint(ctx context.Context, network, address string, proxyURL *url.URL, tlsConfig *tls.Config) (net.Conn, error) {
	if proxyURL == nil {
		return b.open(ctx, network, address)
	}
	if proxyURL.Scheme == "socks5" || proxyURL.Scheme == "socks5h" {
		return b.dialSOCKS(ctx, network, address, proxyURL)
	}
	if proxyURL.Scheme != forwardHTTPScheme && proxyURL.Scheme != forwardHTTPSScheme && proxyURL.Scheme != "" {
		return nil, fmt.Errorf("unsupported forwarding proxy scheme")
	}
	connection, err := b.open(ctx, network, forwardProxyAddress(proxyURL))
	if err != nil {
		return nil, err
	}
	if proxyURL.Scheme == forwardHTTPSScheme {
		config := tlsConfig.Clone()
		config.ServerName = proxyURL.Hostname()
		secure := tls.Client(connection, config)
		if err = secure.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return nil, err
		}
		connection = secure
	}
	if err = forwardProxyCONNECT(ctx, connection, address, proxyURL); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func forwardProxyCONNECT(ctx context.Context, connection net.Conn, address string, proxyURL *url.URL) error {
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	request = request.WithContext(ctx)
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username()+":"+password)))
	}
	if err := request.Write(connection); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		return err
	}
	// CONNECT has transferred ownership of the socket. Closing a successful
	// response body would close the established tunnel before its TLS handshake.
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return fmt.Errorf("forwarding proxy refused CONNECT (status %d)", response.StatusCode)
	}
	return nil
}

func forwardProxyAddress(endpoint *url.URL) string {
	if endpoint.Port() != "" {
		return endpoint.Host
	}
	if endpoint.Scheme == forwardHTTPSScheme {
		return net.JoinHostPort(endpoint.Hostname(), "443")
	}
	return net.JoinHostPort(endpoint.Hostname(), "80")
}

type forwardSOCKSDialer struct{ book *forwardSocketBook }

func (forwardSOCKSDialer) Dial(_, _ string) (net.Conn, error) {
	return nil, fmt.Errorf("forwarding proxy requires a context-aware dial")
}

func (d forwardSOCKSDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.book.open(ctx, network, address)
}

func (b *forwardSocketBook) dialSOCKS(ctx context.Context, network, address string, endpoint *url.URL) (net.Conn, error) {
	var auth *proxy.Auth
	if endpoint.User != nil {
		password, _ := endpoint.User.Password()
		auth = &proxy.Auth{User: endpoint.User.Username(), Password: password}
	}
	dialer, err := proxy.SOCKS5("tcp", forwardProxyAddress(endpoint), auth, forwardSOCKSDialer{book: b})
	if err != nil {
		return nil, err
	}
	return dialer.(proxy.ContextDialer).DialContext(ctx, network, address)
}
