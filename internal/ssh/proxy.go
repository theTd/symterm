package ssh

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/proxy"
)

// proxyDialContext dials addr through a system proxy when one is configured.
// Supported proxy types: SOCKS5 (ALL_PROXY) and HTTP CONNECT (HTTP_PROXY/HTTPS_PROXY).
// The NO_PROXY environment variable is respected for both types.
func proxyDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	// 1. HTTP CONNECT proxy (handles NO_PROXY automatically).
	httpProxyURL, err := httpProxyForAddr(addr)
	if err != nil {
		return nil, err
	}
	if httpProxyURL != nil {
		return dialHTTPConnect(ctx, httpProxyURL, addr)
	}

	// 2. SOCKS5 proxy.
	if socks5URL := allProxyURL(); socks5URL != nil {
		if shouldBypassProxy(addr) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}
		return dialSOCKS5(ctx, socks5URL, network, addr)
	}

	// 3. Direct connection.
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func allProxyURL() *url.URL {
	for _, name := range []string{"ALL_PROXY", "all_proxy"} {
		if v := os.Getenv(name); v != "" {
			u, err := url.Parse(v)
			if err == nil && (u.Scheme == "socks5" || u.Scheme == "socks5h") {
				return u
			}
		}
	}
	return nil
}

func httpProxyForAddr(addr string) (*url.URL, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: host}}
	return http.ProxyFromEnvironment(req)
}

func shouldBypassProxy(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	noProxy := os.Getenv("NO_PROXY")
	if noProxy == "" {
		noProxy = os.Getenv("no_proxy")
	}
	if noProxy == "" {
		return false
	}

	for _, p := range strings.Split(noProxy, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		p = strings.TrimPrefix(p, ".")
		if strings.EqualFold(host, p) {
			return true
		}
		if strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func dialSOCKS5(ctx context.Context, proxyURL *url.URL, network, addr string) (net.Conn, error) {
	var auth *proxy.Auth
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		auth = &proxy.Auth{
			User:     proxyURL.User.Username(),
			Password: password,
		}
	}

	baseDialer := &contextDialer{ctx: ctx}
	dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, baseDialer)
	if err != nil {
		return nil, err
	}

	return dialWithContext(ctx, func() (net.Conn, error) {
		return dialer.Dial(network, addr)
	})
}

type contextDialer struct {
	ctx context.Context
}

func (d *contextDialer) Dial(network, addr string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(d.ctx, network, addr)
}

func dialHTTPConnect(ctx context.Context, proxyURL *url.URL, addr string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", proxyURL.Host)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", proxyURL.Host, err)
	}

	req, err := http.NewRequest("CONNECT", "http://"+addr, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Header.Set("Proxy-Connection", "Keep-Alive")

	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		req.Header.Set("Proxy-Authorization", basicAuth(proxyURL.User.Username(), password))
	}

	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT request: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT %s returned %s", proxyURL.Host, resp.Status)
	}

	return conn, nil
}

func basicAuth(username, password string) string {
	auth := username + ":" + password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(auth))
}

func dialWithContext(ctx context.Context, dial func() (net.Conn, error)) (net.Conn, error) {
	done := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		c, e := dial()
		done <- struct {
			conn net.Conn
			err  error
		}{c, e}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-done:
		return r.conn, r.err
	}
}
