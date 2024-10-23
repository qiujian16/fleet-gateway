package search

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"k8s.io/apimachinery/pkg/api/meta"
	"net/http"

	"github.com/machinebox/graphql"
	"github.com/qiujian16/fleet-gateway/pkg/api"
	"github.com/qiujian16/fleet-gateway/pkg/client/search/options"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metatable "k8s.io/apimachinery/pkg/api/meta/table"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"
)

const clusterLabelKey = "open-cluster-management/cluster"

var queryPattern = `
query searchResultItems($input: [SearchInput]) {
  searchResult: search(input: $input) {
    items
  },
}
`

var resourceInfos = map[schema.GroupVersion][]api.ResourceInfo{
	schema.GroupVersion{Group: "apps", Version: "v1"}: {
		{
			Name:     "deployments",
			Singular: "deployment",
			ListKind: "DeploymentList",
			Kind:     "Deployment",
			Scope:    apiextensionsv1.NamespaceScoped,
			Converter: api.NewConverter(
				[]api.ResourceColumns{
					{Name: "Ready", Type: "string", JSONPath: ".ready"},
					{Name: "UP-TO-DATE", Type: "string", JSONPath: ".current"},
					{Name: "Available", Type: "string", JSONPath: ".available"},
					{Name: "Age", Type: "date", JSONPath: ".created"},
				},
			),
		},
	},
	schema.GroupVersion{Version: "v1"}: {
		{
			Name:     "pods",
			Singular: "pod",
			ListKind: "PodList",
			Kind:     "Pod",
			Scope:    apiextensionsv1.NamespaceScoped,
			Converter: api.NewConverter(
				[]api.ResourceColumns{
					{Name: "Status", Type: "string", JSONPath: ".status"},
					{Name: "Restarts", Type: "string", JSONPath: ".restarts"},
					{Name: "Age", Type: "date", JSONPath: ".created"},
				},
			),
		},
		{
			Name:     "namespaces",
			Singular: "namespace",
			ListKind: "NamespaceList",
			Kind:     "Namespace",
			Scope:    apiextensionsv1.ClusterScoped,
			Converter: api.NewConverter(
				[]api.ResourceColumns{
					{Name: "Age", Type: "date", JSONPath: ".created"},
				},
			),
		},
		{
			Name:     "secrets",
			Singular: "secret",
			ListKind: "SecretList",
			Kind:     "Secret",
			Scope:    apiextensionsv1.NamespaceScoped,
			Converter: api.NewConverter(
				[]api.ResourceColumns{
					{Name: "Age", Type: "date", JSONPath: ".created"},
				},
			),
		},
		{
			Name:     "configmaps",
			Singular: "configmap",
			ListKind: "ConfigMapList",
			Kind:     "ConfigMap",
			Scope:    apiextensionsv1.NamespaceScoped,
			Converter: api.NewConverter(
				[]api.ResourceColumns{
					{Name: "Age", Type: "date", JSONPath: ".created"},
				},
			),
		},
	},
}

type ConverterFunc func(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error)

type Client interface {
	List(ctx context.Context, gvr schema.GroupVersionResource, listOptions metav1.ListOptions) (runtime.Object, error)

	Resources() map[schema.GroupVersion][]api.ResourceInfo

	PrinterfFor(gvr schema.GroupVersionResource) ConverterFunc
}

type SearchVariable struct {
	Keywords []string `json:"keywords"`
	Filters  []Filter `json:"filters"`
	Limit    int32    `json:"limit"`
}

type Filter struct {
	Property string   `json:"property"`
	Values   []string `json:"values"`
}

type ResultData struct {
	SearchResults []SearchResult `json:"searchResult"`
}

type SearchResult struct {
	Items []map[string]interface{} `json:"items"`
}

type Object struct {
	Name       string      `json:"name"`
	Namespace  string      `json:"namespace"`
	Cluster    string      `json:"cluster"`
	APIGroup   string      `json:"apigroup"`
	APIVersion string      `json:"apiversion"`
	Kind       string      `json:"kind"`
	Created    metav1.Time `json:"created"`
}

type searchClient struct {
	client         *graphql.Client
	token          string
	resourcesInfos map[schema.GroupVersion][]api.ResourceInfo
}

func NewSearchClient(o *options.SearchOption) Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	opts := graphql.WithHTTPClient(client)
	return &searchClient{
		client:         graphql.NewClient(o.SearchHost, opts),
		token:          o.Token,
		resourcesInfos: resourceInfos,
	}
}

func (s *searchClient) List(ctx context.Context, gvr schema.GroupVersionResource, listOptions metav1.ListOptions) (runtime.Object, error) {
	ns, ok := apirequest.NamespaceFrom(ctx)
	filters := []Filter{
		{
			Property: "kind_plural",
			Values:   []string{gvr.Resource},
		},
		{
			Property: "apiversion",
			Values:   []string{gvr.Version},
		},
	}
	if ok && ns != "" {
		filters = append(filters, Filter{
			Property: "namespace",
			Values:   []string{ns},
		})
	}
	if gvr.Group != "" {
		filters = append(filters, Filter{
			Property: "apigroup",
			Values:   []string{gvr.Group},
		})
	}
	req := graphql.NewRequest(queryPattern)
	vars := []SearchVariable{
		{
			Keywords: []string{},
			Filters:  filters,
			Limit:    100,
		},
	}
	req.Var("input", vars)
	varStr, _ := json.Marshal(vars)
	klog.Infof("search variable is %s", string(varStr))
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.token))

	respData := ResultData{}
	if err := s.client.Run(ctx, req, &respData); err != nil {
		return nil, err
	}
	res, _ := json.Marshal(respData)
	klog.Infof("result %v", string(res))

	list := &metav1.PartialObjectMetadataList{}
	for _, result := range respData.SearchResults {
		for _, item := range result.Items {
			var apiGroup string
			if item["apigroup"] != nil {
				apiGroup = item["apigroup"].(string)
			}
			version := item["apiversion"].(string)
			apiVersion := apiGroup + "/" + version
			if apiGroup == "" {
				apiVersion = version
			}
			rawData, _ := json.Marshal(item)
			o := metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					Kind:       item["kind"].(string),
					APIVersion: apiVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      item["name"].(string),
					Namespace: item["namespace"].(string),
					Annotations: map[string]string{
						"rawdata": string(rawData),
					},
					Labels: map[string]string{
						clusterLabelKey: item["cluster"].(string),
					},
				},
			}
			var timestamp metav1.Time
			err := timestamp.UnmarshalQueryParameter(item["created"].(string))
			if err != nil {
				continue
			}
			o.CreationTimestamp = timestamp
			list.Items = append(list.Items, o)
		}
	}
	return list, nil
}

func (s *searchClient) Resources() map[schema.GroupVersion][]api.ResourceInfo {
	return s.resourcesInfos
}

func (s *searchClient) PrinterfFor(gvr schema.GroupVersionResource) ConverterFunc {
	gv := schema.GroupVersion{Group: gvr.Group, Version: gvr.Version}
	resources, ok := s.resourcesInfos[gv]
	if !ok {
		return defaultConverter
	}
	for _, resource := range resources {
		if resource.Name == gvr.Resource {
			return resource.Converter.ConvertToTable
		}
	}
	return defaultConverter
}

func defaultConverter(_ context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
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
