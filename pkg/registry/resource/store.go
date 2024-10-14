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
}

func NewStorage(gvr schema.GroupVersionResource, client proxy.Client, seachClient search.Client) ResourceStorage {
	var storage ResourceStorage
	storage.Resource = &REST{gvr: gvr, proxyClient: client, searchClient: seachClient}

	return storage
}

// REST implements a RESTStorage for API services against etcd
type REST struct {
	gvr          schema.GroupVersionResource
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
	return list
}

// List retrieves a list of managedCluster that match label.
func (s *REST) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	v1ListOptions := &metav1.ListOptions{}
	if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, v1ListOptions, nil); err != nil {
		return nil, err
	}
	listOptions := metav1.ListOptions{}
	if v1ListOptions != nil {
		listOptions = *v1ListOptions
	}
	return s.searchClient.List(ctx, s.gvr, listOptions)
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
		clusterName, err := getClusterFromMeta(obj)
		if err != nil {
			return nil, err
		}

		return []interface{}{clusterName, name, age}, nil
	})
	if err != nil {
		return nil, err
	}

	return table, nil
}

var _ = rest.Watcher(&REST{})

func (c *REST) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {

	return nil, fmt.Errorf("watch is not supported")
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
