package options

import "github.com/spf13/cobra"

type ProxyOption struct {
	ProxyServerHost string
	ProxyServerPort int

	ProxyCACertPath string
	ProxyCertPath   string
	ProxyKeyPath    string
}

func NewProxyOption() *ProxyOption {
	return &ProxyOption{}
}

func (p *ProxyOption) AddFlags(cmd *cobra.Command) {
	flags := cmd.Flags()

	flags.StringVar(&p.ProxyServerHost, "cluster-proxy-host", p.ProxyServerHost, "The host of the ANP proxy-server")
	flags.IntVar(&p.ProxyServerPort, "cluster-proxy-port", p.ProxyServerPort, "The port of the ANP proxy-server")

	flags.StringVar(&p.ProxyCACertPath, "cluster-proxy-ca-cert", p.ProxyCACertPath, "The path to the CA certificate of the ANP proxy-server")
	flags.StringVar(&p.ProxyCertPath, "cluster-proxy-client-cert", p.ProxyCertPath, "The path to the certificate of the ANP proxy-server")
	flags.StringVar(&p.ProxyKeyPath, "cluster-proxy-client-key", p.ProxyKeyPath, "The path to the key of the ANP proxy-server")
}
