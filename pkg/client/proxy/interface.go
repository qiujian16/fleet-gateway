package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/pkg/errors"
	"github.com/qiujian16/fleet-gateway/pkg/client/proxy/options"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	msaclientset "open-cluster-management.io/managed-serviceaccount/pkg/generated/clientset/versioned"
)

const tokenName = "gateway-service"

type Client interface {
	DynamicClient(cluster string) (dynamic.Interface, error)
	Proxy(cluster string, uri string, w http.ResponseWriter, req *http.Request) error
}

type proxyClient struct {
	options *options.ProxyOption
	cfg     *rest.Config
}

func NewClient(o *options.ProxyOption, cfg *rest.Config) Client {
	return newClient(o, cfg)
}

func newClient(o *options.ProxyOption, cfg *rest.Config) *proxyClient {
	return &proxyClient{
		options: o,
		cfg:     cfg,
	}
}

func (p *proxyClient) DynamicClient(cluster string) (dynamic.Interface, error) {
	token, err := p.getManagedServiceAccountToken(cluster)
	cfg := &rest.Config{
		// The `ProxyServiceHost` normally is the service domain name of the cluster-proxy-addon user-server:
		// cluster-proxy-addon-user.<component namespace>.svc:9092
		Host: fmt.Sprintf("https://%s/%s", p.options.ProxyServerHost, cluster),
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
		BearerToken: token,
	}

	client, err := dynamic.NewForConfig(cfg)
	return client, err
}

func (p *proxyClient) Proxy(cluster string, uri string, w http.ResponseWriter, req *http.Request) error {
	targetURL := &url.URL{
		Scheme: "https",
		Host:   p.options.ProxyServerHost,
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
		http.Error(rw, fmt.Sprintf("proxy to anp-proxy-server %s failed because %v", targetURL, e), http.StatusBadGateway)
		klog.Errorf("proxy to anp-proxy-server failed because %v", e)
	}

	token, err := p.getManagedServiceAccountToken(cluster)
	if err != nil {
		return err
	}

	req.Host = p.options.ProxyServerHost
	req.URL.Host = p.options.ProxyServerHost
	req.URL.Path = fmt.Sprintf("%s%s", cluster, uri)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	klog.Infof("token to anp %v", token)
	proxy.ServeHTTP(w, req)
	return nil
}

func (p *proxyClient) getManagedServiceAccountToken(namespace string) (string, error) {
	msaClient, err := msaclientset.NewForConfig(p.cfg)
	if err != nil {
		return "", err
	}

	msa, err := msaClient.AuthenticationV1beta1().ManagedServiceAccounts(namespace).Get(context.TODO(), tokenName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	kubeClient, err := kubernetes.NewForConfig(p.cfg)
	if err != nil {
		return "", err
	}
	secret, err := kubeClient.CoreV1().Secrets(namespace).Get(context.TODO(), msa.Status.TokenSecretRef.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	token, ok := secret.Data["token"]
	if !ok {
		return "", errors.Errorf("token is not found in secret %s", secret.Name)
	}

	return string(token), nil
}
