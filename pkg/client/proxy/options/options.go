package options

import (
	"github.com/spf13/pflag"
)

type ProxyOption struct {
	ProxyServerHost string
}

func NewProxyOption() *ProxyOption {
	return &ProxyOption{}
}

func (p *ProxyOption) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&p.ProxyServerHost, "cluster-proxy-host", p.ProxyServerHost, "The host of the ANP proxy-server")
}
