/*
Copyright 2017 The Kubernetes Authors.

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

package apiserver

import (
	"net/http"
	"time"

	"github.com/qiujian16/fleet-gateway/pkg/client/proxy"
	"github.com/qiujian16/fleet-gateway/pkg/client/search"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/apiserver/pkg/endpoints/discovery/aggregated"
	genericapiserver "k8s.io/apiserver/pkg/server"
	serverstorage "k8s.io/apiserver/pkg/server/storage"
)

var (
	Scheme = runtime.NewScheme()
	Codecs = serializer.NewCodecFactory(Scheme)

	// if you modify this, make sure you update the crEncoder
	unversionedVersion = schema.GroupVersion{Group: "", Version: "v1"}
	unversionedTypes   = []runtime.Object{
		&metav1.Status{},
		&metav1.WatchEvent{},
		&metav1.APIVersions{},
		&metav1.APIGroupList{},
		&metav1.APIGroup{},
		&metav1.APIResourceList{},
	}
)

func init() {
	// we need to add the options to empty v1
	metav1.AddToGroupVersion(Scheme, schema.GroupVersion{Group: "", Version: "v1"})

	Scheme.AddUnversionedTypes(unversionedVersion, unversionedTypes...)
}

type Config struct {
	GenericConfig *genericapiserver.RecommendedConfig
	ProxyClient   proxy.Client
	SearchClient  search.Client
}

type completedConfig struct {
	GenericConfig genericapiserver.CompletedConfig
	ProxyClient   proxy.Client
	SearchClient  search.Client
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

type Gateway struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (cfg *Config) Complete() CompletedConfig {
	c := completedConfig{
		GenericConfig: cfg.GenericConfig.Complete(),
		ProxyClient:   cfg.ProxyClient,
		SearchClient:  cfg.SearchClient,
	}

	c.GenericConfig.EnableDiscovery = false

	return CompletedConfig{&c}
}

// New returns a new instance of CustomResourceDefinitions from the given config.
func (c completedConfig) New(delegationTarget genericapiserver.DelegationTarget) (*Gateway, error) {
	genericServer, err := c.GenericConfig.New("fleed-gateway-apiserver", delegationTarget)
	if err != nil {
		return nil, err
	}

	s := &Gateway{
		GenericAPIServer: genericServer,
	}

	delegateHandler := delegationTarget.UnprotectedHandler()
	if delegateHandler == nil {
		delegateHandler = http.NotFoundHandler()
	}

	versionDiscoveryHandler := &versionDiscoveryHandler{
		discovery: map[schema.GroupVersion]*discovery.APIVersionHandler{},
		delegate:  delegateHandler,
	}
	groupDiscoveryHandler := &groupDiscoveryHandler{
		discovery: map[string]*discovery.APIGroupHandler{},
		delegate:  delegateHandler,
	}
	crdHandler, err := NewResourceHandler(
		versionDiscoveryHandler,
		groupDiscoveryHandler,
		delegateHandler,
		s.GenericAPIServer.Authorizer,
		c.ProxyClient,
		c.SearchClient,
		c.GenericConfig.RequestTimeout,
		time.Duration(c.GenericConfig.MinRequestTimeout)*time.Second,
		c.GenericConfig.MaxRequestBodyBytes,
	)
	if err != nil {
		return nil, err
	}
	s.GenericAPIServer.Handler.NonGoRestfulMux.Handle("/apis", crdHandler)
	s.GenericAPIServer.Handler.NonGoRestfulMux.HandlePrefix("/apis/", crdHandler)
	s.GenericAPIServer.Handler.NonGoRestfulMux.HandlePrefix("/api/", crdHandler)
	s.GenericAPIServer.Handler.NonGoRestfulMux.HandlePrefix("/clusters/", crdHandler)
	s.GenericAPIServer.RegisterDestroyFunc(crdHandler.destroy)

	aggregatedDiscoveryManager := genericServer.AggregatedDiscoveryGroupManager
	if aggregatedDiscoveryManager != nil {
		aggregatedDiscoveryManager = aggregatedDiscoveryManager.WithSource(aggregated.CRDSource)
	}
	discoveryController := NewDiscoveryController(versionDiscoveryHandler, groupDiscoveryHandler, aggregatedDiscoveryManager)

	s.GenericAPIServer.AddPostStartHookOrDie("start-fleet-gateway-controllers", func(context genericapiserver.PostStartHookContext) error {
		discoverySyncedCh := make(chan struct{})
		go discoveryController.Run(context.StopCh, discoverySyncedCh)
		select {
		case <-context.StopCh:
		case <-discoverySyncedCh:
		}

		return nil
	})

	return s, nil
}

func DefaultAPIResourceConfigSource() *serverstorage.ResourceConfig {
	ret := serverstorage.NewResourceConfig()

	return ret
}
