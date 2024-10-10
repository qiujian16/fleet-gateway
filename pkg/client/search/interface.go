package search

import (
	"context"
	"fmt"

	"github.com/machinebox/graphql"
	"github.com/qiujian16/fleet-gateway/pkg/api"
	"github.com/qiujian16/fleet-gateway/pkg/client/search/options"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var queryPattern = `
query searchResultItems($input: [SearchInput]) {
  searchResult: search(input: $input) {
    items
  },
}
`

type Client interface {
	List(ctx context.Context, gvr schema.GroupVersionResource, listOptions metav1.ListOptions) (runtime.Object, error)

	Resources() []api.ResourceInfo

	ResoursesFor(name string) api.ResourceInfo
}

type SearchVariable struct {
	Keywords []string `json:"keywords"`
	Filters  []Filter `json:"filters"`
}

type Filter struct {
	Property string   `json:"property"`
	Values   []string `json:"values"`
	Limit    int32    `json:"limit"`
}

type Result struct {
	Data ResultData `json:"data"`
}

type ResultData struct {
	SearchResults []SearchResult `json:"searchResult"`
}

type SearchResult struct {
	Items []Object `json:"items"`
}

type Object struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Cluster    string `json:"cluster"`
	APIVersion string `json:"apiversion"`
	Kind       string `json:"kind"`
}

type searchClient struct {
	client *graphql.Client
	token  string
}

func NewSearchClient(o *options.SearchOption) Client {
	return &searchClient{
		client: graphql.NewClient(o.SearchHost),
		token:  o.Token,
	}
}

func (s *searchClient) List(ctx context.Context, gvr schema.GroupVersionResource, listOptions metav1.ListOptions) (runtime.Object, error) {
	req := graphql.NewRequest(queryPattern)
	vars := []SearchVariable{
		{
			Keywords: []string{},
			Filters: []Filter{
				{
					Property: "kind_plural",
					Values:   []string{gvr.Resource},
					Limit:    100,
				},
			},
		},
	}
	req.Var("input", vars)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.token))

	var respData Result
	if err := s.client.Run(ctx, req, &respData); err != nil {
		return nil, err
	}

	list := &metav1.PartialObjectMetadataList{}
	for _, result := range respData.Data.SearchResults {
		for _, item := range result.Items {
			o := metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					Kind:       item.Kind,
					APIVersion: item.APIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      item.Name,
					Namespace: item.Namespace,
					Labels: map[string]string{
						"open-cluster-management/cluster": item.Cluster,
					},
				},
			}
			list.Items = append(list.Items, o)
		}
	}
	return list, nil
}

func (s *searchClient) Resources() []api.ResourceInfo {
	return []api.ResourceInfo{}
}

func (s *searchClient) ResoursesFor(name string) api.ResourceInfo {
	return api.ResourceInfo{}
}
