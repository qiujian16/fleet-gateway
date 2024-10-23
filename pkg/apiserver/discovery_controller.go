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
	apidiscoveryv2 "k8s.io/api/apidiscovery/v2"
	"k8s.io/apiserver/pkg/endpoints/discovery/aggregated"
	"sort"
	"time"

	"github.com/qiujian16/fleet-gateway/pkg/client/search"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/klog/v2"
)

type DiscoveryController struct {
	versionHandler  *versionDiscoveryHandler
	groupHandler    *groupDiscoveryHandler
	resourceManager aggregated.ResourceManager
	searchClient    search.Client
}

func NewDiscoveryController(
	versionHandler *versionDiscoveryHandler,
	groupHandler *groupDiscoveryHandler,
	resourceManager aggregated.ResourceManager,
	searchClient search.Client,
) *DiscoveryController {
	c := &DiscoveryController{
		versionHandler:  versionHandler,
		groupHandler:    groupHandler,
		resourceManager: resourceManager,
		searchClient:    searchClient,
	}

	return c
}

func (c *DiscoveryController) sync() {
	// read resourceinfos from search
	resourceInfos := c.searchClient.Resources()
	verbs := metav1.Verbs([]string{"list"})
	for gv, resources := range resourceInfos {
		apiVersionsForDiscovery := []metav1.GroupVersionForDiscovery{}
		apiResourcesForDiscovery := []metav1.APIResource{}
		aggregatedAPIResourcesForDiscovery := []apidiscoveryv2.APIResourceDiscovery{}
		for _, resource := range resources {
			groupVersion := gv.Group + "/" + gv.Version
			if len(gv.Group) == 0 {
				groupVersion = gv.Version
			}
			apiVersionsForDiscovery = append(apiVersionsForDiscovery, metav1.GroupVersionForDiscovery{
				GroupVersion: groupVersion,
				Version:      gv.Version,
			})

			apiResourcesForDiscovery = append(apiResourcesForDiscovery, metav1.APIResource{
				Name:         resource.Name,
				SingularName: resource.Singular,
				Namespaced:   resource.Scope == apiextensionsv1.NamespaceScoped,
				Kind:         resource.Kind,
				Verbs:        verbs,
			})
			c.versionHandler.setDiscovery(gv, discovery.NewAPIVersionHandler(Codecs, gv, discovery.APIResourceListerFunc(func() []metav1.APIResource {
				return apiResourcesForDiscovery
			})))

			verbs := metav1.Verbs([]string{"list"})
			if gv.Group != "" {
				var scope apidiscoveryv2.ResourceScope
				if resource.Scope == apiextensionsv1.NamespaceScoped {
					scope = apidiscoveryv2.ScopeNamespace
				} else {
					scope = apidiscoveryv2.ScopeCluster
				}
				apiResourceDiscovery := apidiscoveryv2.APIResourceDiscovery{
					Resource:         resource.Name,
					SingularResource: resource.Singular,
					Scope:            scope,
					ResponseKind: &metav1.GroupVersionKind{
						Group:   gv.Group,
						Version: gv.Version,
						Kind:    resource.Kind,
					},
					Verbs: verbs,
				}
				aggregatedAPIResourcesForDiscovery = append(aggregatedAPIResourcesForDiscovery, apiResourceDiscovery)
			}
		}

		sortGroupDiscoveryByKubeAwareVersion(apiVersionsForDiscovery)

		apiGroup := metav1.APIGroup{
			Name:     gv.Group,
			Versions: apiVersionsForDiscovery,
			// the preferred versions for a group is the first item in
			// apiVersionsForDiscovery after it put in the right ordered
			PreferredVersion: apiVersionsForDiscovery[0],
		}
		c.groupHandler.setDiscovery(gv.Group, discovery.NewAPIGroupHandler(Codecs, apiGroup))
		if c.resourceManager != nil {
			if gv.Group != "" {
				c.resourceManager.AddGroupVersion(gv.Group, apidiscoveryv2.APIVersionDiscovery{
					Freshness: apidiscoveryv2.DiscoveryFreshnessCurrent,
					Version:   gv.Version,
					Resources: aggregatedAPIResourcesForDiscovery,
				})
			}
		}
	}
	return
}

func sortGroupDiscoveryByKubeAwareVersion(gd []metav1.GroupVersionForDiscovery) {
	sort.Slice(gd, func(i, j int) bool {
		return version.CompareKubeAwareVersionStrings(gd[i].Version, gd[j].Version) > 0
	})
}

func (c *DiscoveryController) Run(stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer klog.Info("Shutting down DiscoveryController")

	klog.Info("Starting DiscoveryController")

	// only start one worker thread since its a slow moving API
	go wait.Until(c.sync, 5*time.Second, stopCh)

	<-stopCh
}
