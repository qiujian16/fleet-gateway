package proxy

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/qiujian16/fleet-gateway/pkg/client/proxy/options"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

type Client interface {
	DynamicClient(cluster string) (dynamic.Interface, error)
	Proxy(cluster string) (*httputil.ReverseProxy, error)
}

type proxyClient struct {
	options *options.ProxyOption
}

func NewClient(o *options.ProxyOption) Client {
	return newClient(o)
}

func newClient(o *options.ProxyOption) *proxyClient {
	return &proxyClient{
		options: o,
	}
}

func (p *proxyClient) DynamicClient(cluster string) (dynamic.Interface, error) {
	cfg := &rest.Config{
		// The `ProxyServiceHost` normally is the service domain name of the cluster-proxy-addon user-server:
		// cluster-proxy-addon-user.<component namespace>.svc:9092
		Host: fmt.Sprintf("https://%s/%s", p.options.ProxyServerHost, cluster),
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
		// TODO add authentication part
		//BearerToken: string(logTokenSecret.Data["token"]),
	}

	client, err := dynamic.NewForConfig(cfg)
	return client, err
}

func (p *proxyClient) Proxy(cluster string) (*httputil.ReverseProxy, error) {
	targetURL, err := url.Parse(fmt.Sprintf("https://%s/%s", p.options.ProxyServerHost, cluster))
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = &http.Transport{
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		// golang http pkg automaticly upgrade http connection to http2 connection, but http2 can not upgrade to SPDY which used in "kubectl exec".
		// set ForceAttemptHTTP2 = false to prevent auto http2 upgration
		ForceAttemptHTTP2: false,
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, e error) {
		http.Error(rw, fmt.Sprintf("proxy to anp-proxy-server failed because %v", e), http.StatusBadGateway)
		klog.Errorf("proxy to anp-proxy-server failed because %v", e)
	}

	return proxy, nil
}
