package proxy

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/qiujian16/fleet-gateway/pkg/client/proxy/options"
	"github.com/stolostron/cluster-proxy-addon/pkg/constant"
	"google.golang.org/grpc"
	grpccredentials "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	clusterproxyutil "open-cluster-management.io/cluster-proxy/pkg/util"
	konnectivity "sigs.k8s.io/apiserver-network-proxy/konnectivity-client/pkg/client"
	"sigs.k8s.io/apiserver-network-proxy/pkg/util"
)

type Client interface {
	DynamicClient(ctx context.Context, cluster string) (dynamic.Interface, error)
}

type proxyClient struct {
	options   *options.ProxyOption
	getTunnel func(context.Context) (konnectivity.Tunnel, error)
}

func NewClient(o *options.ProxyOption) Client {
	return newClient(o)
}

func newClient(o *options.ProxyOption) *proxyClient {
	return &proxyClient{
		options: o,
	}
}

func (p *proxyClient) DynamicClient(ctx context.Context, cluster string) (dynamic.Interface, error) {
	proxyTLSCfg, err := util.GetClientTLSConfig(p.options.ProxyCACertPath, p.options.ProxyCertPath, p.options.ProxyKeyPath, p.options.ProxyServerHost)
	if err != nil {
		return nil, err
	}

	tunnel, err := konnectivity.CreateSingleUseGrpcTunnelWithContext(
		context.Background(),
		ctx,
		net.JoinHostPort(p.options.ProxyServerHost, strconv.Itoa(p.options.ProxyServerPort)),
		grpc.WithTransportCredentials(grpccredentials.NewTLS(proxyTLSCfg)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time: time.Second * 5,
		}),
	)
	if err != nil {
		return nil, err
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		return nil, err
	}
	// The managed cluster's name.
	host := fmt.Sprintf("https://%s:%d", clusterproxyutil.GenerateServiceURL(
		cluster, "open-cluster-management-agent-addon", constant.ServiceProxyName), constant.ServiceProxyPort)

	cfg.Host = host
	cfg.Insecure = true
	// Override the default tcp dialer
	cfg.Dial = tunnel.DialContext

	// TODO add authentication part

	client, err := dynamic.NewForConfig(cfg)
	return client, err
}
