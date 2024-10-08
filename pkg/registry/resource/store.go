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

package customresource

import (
	"context"
	"fmt"

	"github.com/qiujian16/fleet-gateway/pkg/client/proxy"
	"github.com/qiujian16/fleet-gateway/pkg/client/search"
	"github.com/qiujian16/fleet-gateway/pkg/request"
	"k8s.io/apimachinery/pkg/api/meta"
	metatable "k8s.io/apimachinery/pkg/api/meta/table"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
)

// ResourceStorage includes dummy storage for CustomResources, and their Status and Scale subresources.
type ResourceStorage struct {
	Resource *REST
	Status   *StatusREST
}

func NewStorage(resource schema.GroupVersionResource, kind, listKind schema.GroupVersionKind) ResourceStorage {
	var storage ResourceStorage
	storage.Resource = &REST{}
	storage.Status = &StatusREST{}

	return storage
}

// REST implements a RESTStorage for API services against etcd
type REST struct {
	gvr          schema.GroupVersionResource
	kind         schema.GroupVersionKind
	listKind     schema.GroupVersionKind
	searchClient search.Client
	proxyClient  proxy.Client
}

// Implement CategoriesProvider
var _ rest.CategoriesProvider = &REST{}

// Categories implements the CategoriesProvider interface. Returns a list of categories a resource is part of.
func (r *REST) Categories() []string {
	return []string{}
}

var _ = rest.Lister(&REST{})

func (s *REST) NewList() runtime.Object {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(s.listKind)
	return list
}

// List retrieves a list of managedCluster that match label.
func (s *REST) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	var v1ListOptions metav1.ListOptions
	if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
		return nil, err
	}

	cluster := request.ClusterFrom(ctx)
	if cluster.Wildcard {
		return s.searchClient.List(ctx, s.gvr, v1ListOptions)
	}

	client, err := s.proxyClient.DynamicClient(cluster.Name)
	if err != nil {
		return nil, err
	}

	return client.Resource(s.gvr).List(ctx, v1ListOptions)
}

func (c *REST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	headers := []metav1.TableColumnDefinition{
		{Name: "Cluster", Type: "string", Format: "name", Description: "Cluster is the cluster of the resource."},
		{Name: "Name", Type: "string", Format: "name", Description: "Name is the name of the resource."},
		{Name: "Age", Type: "date", Description: "Age represents the age of the manifestworks until created."},
	}
	table := &metav1.Table{}
	opt, ok := tableOptions.(*metav1.TableOptions)
	noHeaders := ok && opt != nil && opt.NoHeaders
	if !noHeaders {
		table.ColumnDefinitions = headers
	}

	if m, err := meta.ListAccessor(object); err == nil {
		table.ResourceVersion = m.GetResourceVersion()
		table.Continue = m.GetContinue()
		table.RemainingItemCount = m.GetRemainingItemCount()
	} else {
		if m, err := meta.CommonAccessor(object); err == nil {
			table.ResourceVersion = m.GetResourceVersion()
		}
	}
	var err error
	table.Rows, err = metatable.MetaToTableRow(object, func(obj runtime.Object, m metav1.Object, name, age string) ([]interface{}, error) {
		cluster := request.ClusterFrom(ctx)
		clusterName := cluster.Name
		if cluster.Wildcard {
			clusterName, err = getClusterFromMeta(obj)
			if err != nil {
				return nil, err
			}
		}

		return []interface{}{clusterName, name, age}, nil
	})
	if err != nil {
		return nil, err
	}

	return table, nil
}

var _ = rest.Getter(&REST{})

func (c *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	cluster := request.ClusterFrom(ctx)
	if cluster.Wildcard {
		return nil, fmt.Errorf("should specify cluster")
	}

	client, err := c.proxyClient.DynamicClient(cluster.Name)
	if err != nil {
		return nil, err
	}

	return client.Resource(c.gvr).Get(ctx, name, *options)
}

var _ = rest.Watcher(&REST{})

func (c *REST) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	cluster := request.ClusterFrom(ctx)
	if cluster.Wildcard {
		return nil, fmt.Errorf("should specify cluster")
	}
	var v1ListOptions metav1.ListOptions
	if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
		return nil, err
	}

	client, err := c.proxyClient.DynamicClient(cluster.Name)
	if err != nil {
		return nil, err
	}

	return client.Resource(c.gvr).Watch(ctx, v1ListOptions)
}

var _ = rest.CreaterUpdater(&REST{})

func (c *REST) New() runtime.Object {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(c.kind)
	return obj
}

func (c *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	cluster := request.ClusterFrom(ctx)
	if cluster.Wildcard {
		return nil, fmt.Errorf("should specify cluster")
	}

	client, err := c.proxyClient.DynamicClient(cluster.Name)
	if err != nil {
		return nil, err
	}

	return client.Resource(c.gvr).Create(ctx, obj.(*unstructured.Unstructured), *options)
}

func (c *REST) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	cluster := request.ClusterFrom(ctx)
	if cluster.Wildcard {
		return nil, false, fmt.Errorf("should specify cluster")
	}

	obj, err := objInfo.UpdatedObject(ctx, nil)
	if err != nil {
		return nil, false, err
	}

	client, err := c.proxyClient.DynamicClient(cluster.Name)
	if err != nil {
		return nil, false, err
	}

	obj, err = client.Resource(c.gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, false, err
	}

	unstructuredObject, err := objInfo.UpdatedObject(ctx, obj)
	if err != nil {
		return nil, false, err
	}

	updated, err := client.Resource(c.gvr).Update(ctx, unstructuredObject.(*unstructured.Unstructured), *options)
	return updated, false, err
}

var _ = rest.GracefulDeleter(&REST{})

func (c *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	cluster := request.ClusterFrom(ctx)
	if cluster.Wildcard {
		return nil, false, fmt.Errorf("should specify cluster")
	}

	client, err := c.proxyClient.DynamicClient(cluster.Name)
	if err != nil {
		return nil, false, err
	}

	obj, err := client.Resource(c.gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, false, err
	}

	err = client.Resource(c.gvr).Delete(ctx, name, *options)
	return obj, false, err
}

// StatusREST implements the REST endpoint for changing the status of a CustomResource
type StatusREST struct {
	gvr         schema.GroupVersionResource
	proxyClient proxy.Client
}

var _ = rest.Patcher(&StatusREST{})

func (r *StatusREST) New() runtime.Object {
	return &unstructured.Unstructured{}
}

// Get retrieves the object from the storage. It is required to support Patch.
func (r *StatusREST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	cluster := request.ClusterFrom(ctx)
	if cluster.Wildcard {
		return nil, fmt.Errorf("should specify cluster")
	}

	client, err := r.proxyClient.DynamicClient(cluster.Name)
	if err != nil {
		return nil, err
	}

	return client.Resource(r.gvr).Get(ctx, name, *options)
}

// Update alters the status subset of an object.
func (r *StatusREST) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	cluster := request.ClusterFrom(ctx)
	if cluster.Wildcard {
		return nil, false, fmt.Errorf("should specify cluster")
	}

	// We are explicitly setting forceAllowCreate to false in the call to the underlying storage because
	// subresources should never allow create on update.
	obj, err := objInfo.UpdatedObject(ctx, nil)
	if err != nil {
		return nil, false, err
	}

	client, err := r.proxyClient.DynamicClient(cluster.Name)
	if err != nil {
		return nil, false, err
	}

	obj, err = client.Resource(r.gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, false, err
	}

	unstructuredObject, err := objInfo.UpdatedObject(ctx, obj)
	if err != nil {
		return nil, false, err
	}

	updated, err := client.Resource(r.gvr).UpdateStatus(ctx, unstructuredObject.(*unstructured.Unstructured), *options)
	return updated, false, err
}

const clusterLabelKey = "open-cluster-management/cluster"

func getClusterFromMeta(obj runtime.Object) (string, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return "", err
	}

	labels := accessor.GetLabels()
	if len(labels) == 0 {
		return "", fmt.Errorf("cluster label does not found")
	}

	cluster, ok := labels[clusterLabelKey]
	if !ok {
		return "", fmt.Errorf("cluster label does not found")
	}

	return cluster, nil
}
