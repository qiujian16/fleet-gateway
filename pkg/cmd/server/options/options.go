/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package options

import (
	"fmt"
	"io"
	"net"

	"github.com/qiujian16/fleet-gateway/pkg/apiserver"
	"github.com/qiujian16/fleet-gateway/pkg/client/proxy"
	proxyoptions "github.com/qiujian16/fleet-gateway/pkg/client/proxy/options"
	searchoptions "github.com/qiujian16/fleet-gateway/pkg/client/search/options"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/tools/clientcmd"
	netutils "k8s.io/utils/net"
)

// CustomResourceDefinitionsServerOptions describes the runtime options of an apiextensions-apiserver.
type GatewayServerOptions struct {
	ServerRunOptions   *genericoptions.ServerRunOptions
	SecureServing      *genericoptions.SecureServingOptionsWithLoopback
	Authentication     *genericoptions.DelegatingAuthenticationOptions
	Authorization      *genericoptions.DelegatingAuthorizationOptions
	CoreAPI            *genericoptions.CoreAPIOptions
	ProxyClientOption  *proxyoptions.ProxyOption
	SearchClientOption *searchoptions.SearchOption

	StdOut io.Writer
	StdErr io.Writer
}

// NewCustomResourceDefinitionsServerOptions creates default options of an apiextensions-apiserver.
func NewGatewayServerOptions(out, errOut io.Writer) *GatewayServerOptions {
	o := &GatewayServerOptions{
		ServerRunOptions:   genericoptions.NewServerRunOptions(),
		SecureServing:      genericoptions.NewSecureServingOptions().WithLoopback(),
		Authentication:     genericoptions.NewDelegatingAuthenticationOptions(),
		Authorization:      genericoptions.NewDelegatingAuthorizationOptions(),
		CoreAPI:            genericoptions.NewCoreAPIOptions(),
		ProxyClientOption:  proxyoptions.NewProxyOption(),
		SearchClientOption: searchoptions.NewSearchOptions(),
		StdOut:             out,
		StdErr:             errOut,
	}

	return o
}

// AddFlags adds the apiextensions-apiserver flags to the flagset.
func (o GatewayServerOptions) AddFlags(fs *pflag.FlagSet) {
	o.ServerRunOptions.AddUniversalFlags(fs)
	o.SecureServing.AddFlags(fs)
	o.Authorization.AddFlags(fs)
	o.Authentication.AddFlags(fs)
	o.CoreAPI.AddFlags(fs)
	o.ProxyClientOption.AddFlags(fs)
	o.SearchClientOption.AddFlags(fs)
}

// Validate validates the apiextensions-apiserver options.
func (o GatewayServerOptions) Validate() error {
	errors := []error{}
	errors = append(errors, o.ServerRunOptions.Validate()...)
	return utilerrors.NewAggregate(errors)
}

// Complete fills in missing options.
func (o *GatewayServerOptions) Complete() error {
	return o.ServerRunOptions.Complete()
}

// Config returns an apiextensions-apiserver configuration.
func (o GatewayServerOptions) Config() (*apiserver.Config, error) {
	// TODO have a "real" external address
	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{netutils.ParseIPSloppy("127.0.0.1")}); err != nil {
		return nil, fmt.Errorf("error creating self-signed certificates: %v", err)
	}

	serverConfig := genericapiserver.NewRecommendedConfig(apiserver.Codecs)
	if err := o.ServerRunOptions.ApplyTo(&serverConfig.Config); err != nil {
		return nil, err
	}
	if err := o.SecureServing.ApplyTo(&serverConfig.Config.SecureServing, &serverConfig.Config.LoopbackClientConfig); err != nil {
		return nil, err
	}
	if err := o.Authentication.ApplyTo(&serverConfig.Config.Authentication, serverConfig.SecureServing, serverConfig.OpenAPIConfig); err != nil {
		return nil, err
	}
	if err := o.Authorization.ApplyTo(&serverConfig.Config.Authorization); err != nil {
		return nil, err
	}
	if err := o.CoreAPI.ApplyTo(serverConfig); err != nil {
		return nil, err
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", o.CoreAPI.CoreAPIKubeconfigPath)
	if err != nil {
		return nil, err
	}

	config := &apiserver.Config{
		GenericConfig: serverConfig,
		ProxyClient:   proxy.NewClient(o.ProxyClientOption, cfg),
	}
	return config, nil
}
