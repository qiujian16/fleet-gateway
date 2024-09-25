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
	"fmt"
	"sort"
	"time"

	"github.com/qiujian16/fleet-gateway/pkg/api"
	apidiscoveryv2 "k8s.io/api/apidiscovery/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	discoveryendpoint "k8s.io/apiserver/pkg/endpoints/discovery/aggregated"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

type DiscoveryController struct {
	versionHandler  *versionDiscoveryHandler
	groupHandler    *groupDiscoveryHandler
	resourceManager discoveryendpoint.ResourceManager

	// To allow injection for testing.
	syncFn func(version schema.GroupVersion) error

	queue workqueue.TypedRateLimitingInterface[schema.GroupVersion]
}

func NewDiscoveryController(
	versionHandler *versionDiscoveryHandler,
	groupHandler *groupDiscoveryHandler,
	resourceManager discoveryendpoint.ResourceManager,
) *DiscoveryController {
	c := &DiscoveryController{
		versionHandler:  versionHandler,
		groupHandler:    groupHandler,
		resourceManager: resourceManager,

		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[schema.GroupVersion](),
			workqueue.TypedRateLimitingQueueConfig[schema.GroupVersion]{Name: "DiscoveryController"},
		),
	}

	c.syncFn = c.sync

	return c
}

func (c *DiscoveryController) sync(version schema.GroupVersion) error {

	apiVersionsForDiscovery := []metav1.GroupVersionForDiscovery{}
	apiResourcesForDiscovery := []metav1.APIResource{}
	aggregatedAPIResourcesForDiscovery := []apidiscoveryv2.APIResourceDiscovery{}
	versionsForDiscoveryMap := map[metav1.GroupVersion]bool{}

	// read resourceinfors from search
	resourceInfos := []api.ResourceInfo{}

	foundVersion := false
	foundGroup := false
	for _, resource := range resourceInfos {
		foundThisVersion := false
		for _, v := range resource.Versions {
			// If there is any Served version, that means the group should show up in discovery
			foundGroup = true

			gv := metav1.GroupVersion{Group: resource.GVR.Group, Version: v}
			if !versionsForDiscoveryMap[gv] {
				versionsForDiscoveryMap[gv] = true
				apiVersionsForDiscovery = append(apiVersionsForDiscovery, metav1.GroupVersionForDiscovery{
					GroupVersion: resource.GVR.Group + "/" + v,
					Version:      v,
				})
			}
			if v == version.Version {
				foundThisVersion = true
			}
		}

		if !foundThisVersion {
			continue
		}
		foundVersion = true

		verbs := metav1.Verbs([]string{"delete", "deletecollection", "get", "list", "patch", "create", "update"})

		apiResourcesForDiscovery = append(apiResourcesForDiscovery, metav1.APIResource{
			Name:         resource.GVR.Resource,
			SingularName: resource.Singular,
			Namespaced:   resource.Scope == apiextensionsv1.NamespaceScoped,
			Kind:         resource.Kind,
			Verbs:        verbs,
		})

		if c.resourceManager != nil {
			var scope apidiscoveryv2.ResourceScope
			if resource.Scope == apiextensionsv1.NamespaceScoped {
				scope = apidiscoveryv2.ScopeNamespace
			} else {
				scope = apidiscoveryv2.ScopeCluster
			}
			apiResourceDiscovery := apidiscoveryv2.APIResourceDiscovery{
				Resource:         resource.GVR.Resource,
				SingularResource: resource.Singular,
				Scope:            scope,
				ResponseKind: &metav1.GroupVersionKind{
					Group:   version.Group,
					Version: version.Version,
					Kind:    resource.Kind,
				},
				Verbs: verbs,
			}
			if resource.SubResources.Has("status") {
				apiResourceDiscovery.Subresources = append(apiResourceDiscovery.Subresources, apidiscoveryv2.APISubresourceDiscovery{
					Subresource: "status",
					ResponseKind: &metav1.GroupVersionKind{
						Group:   version.Group,
						Version: version.Version,
						Kind:    resource.Kind,
					},
					Verbs: []string{"get", "patch", "update"},
				})
			}
			aggregatedAPIResourcesForDiscovery = append(aggregatedAPIResourcesForDiscovery, apiResourceDiscovery)
		}

		if resource.SubResources.Has("status") {
			apiResourcesForDiscovery = append(apiResourcesForDiscovery, metav1.APIResource{
				Name:       resource.GVR.Resource + "/status",
				Namespaced: resource.Scope == apiextensionsv1.NamespaceScoped,
				Kind:       resource.Kind,
				Verbs:      []string{"get", "patch", "update"},
			})
		}
	}

	if !foundGroup {
		c.groupHandler.unsetDiscovery(version.Group)
		c.versionHandler.unsetDiscovery(version)

		if c.resourceManager != nil {
			c.resourceManager.RemoveGroup(version.Group)
		}
		return nil
	}

	sortGroupDiscoveryByKubeAwareVersion(apiVersionsForDiscovery)

	apiGroup := metav1.APIGroup{
		Name:     version.Group,
		Versions: apiVersionsForDiscovery,
		// the preferred versions for a group is the first item in
		// apiVersionsForDiscovery after it put in the right ordered
		PreferredVersion: apiVersionsForDiscovery[0],
	}
	c.groupHandler.setDiscovery(version.Group, discovery.NewAPIGroupHandler(Codecs, apiGroup))

	if !foundVersion {
		c.versionHandler.unsetDiscovery(version)

		if c.resourceManager != nil {
			c.resourceManager.RemoveGroupVersion(metav1.GroupVersion{
				Group:   version.Group,
				Version: version.Version,
			})
		}
		return nil
	}
	c.versionHandler.setDiscovery(version, discovery.NewAPIVersionHandler(Codecs, version, discovery.APIResourceListerFunc(func() []metav1.APIResource {
		return apiResourcesForDiscovery
	})))

	sort.Slice(aggregatedAPIResourcesForDiscovery, func(i, j int) bool {
		return aggregatedAPIResourcesForDiscovery[i].Resource < aggregatedAPIResourcesForDiscovery[j].Resource
	})
	if c.resourceManager != nil {
		c.resourceManager.AddGroupVersion(version.Group, apidiscoveryv2.APIVersionDiscovery{
			Freshness: apidiscoveryv2.DiscoveryFreshnessCurrent,
			Version:   version.Version,
			Resources: aggregatedAPIResourcesForDiscovery,
		})
		// Default priority for CRDs
		c.resourceManager.SetGroupVersionPriority(metav1.GroupVersion(version), 1000, 100)
	}
	return nil
}

func sortGroupDiscoveryByKubeAwareVersion(gd []metav1.GroupVersionForDiscovery) {
	sort.Slice(gd, func(i, j int) bool {
		return version.CompareKubeAwareVersionStrings(gd[i].Version, gd[j].Version) > 0
	})
}

func (c *DiscoveryController) Run(stopCh <-chan struct{}, synchedCh chan<- struct{}) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()
	defer klog.Info("Shutting down DiscoveryController")

	klog.Info("Starting DiscoveryController")

	// initially sync all group versions to make sure we serve complete discovery
	if err := wait.PollImmediateUntil(time.Second, func() (bool, error) {
		// get all resources types in the search
		return true, nil
	}, stopCh); err == wait.ErrWaitTimeout {
		utilruntime.HandleError(fmt.Errorf("timed out waiting for discovery endpoint to initialize"))
		return
	} else if err != nil {
		panic(fmt.Errorf("unexpected error: %v", err))
	}
	close(synchedCh)

	// only start one worker thread since its a slow moving API
	go wait.Until(c.runWorker, time.Second, stopCh)

	<-stopCh
}

func (c *DiscoveryController) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem deals with one key off the queue.  It returns false when it's time to quit.
func (c *DiscoveryController) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.syncFn(key)
	if err == nil {
		c.queue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("%v failed with: %v", key, err))
	c.queue.AddRateLimited(key)

	return true
}

func (c *DiscoveryController) enqueue(obj *apiextensionsv1.CustomResourceDefinition) {
	for _, v := range obj.Spec.Versions {
		c.queue.Add(schema.GroupVersion{Group: obj.Spec.Group, Version: v.Name})
	}
}

func (c *DiscoveryController) addCustomResourceDefinition(obj interface{}) {
	castObj := obj.(*apiextensionsv1.CustomResourceDefinition)
	klog.V(4).Infof("Adding customresourcedefinition %s", castObj.Name)
	c.enqueue(castObj)
}
