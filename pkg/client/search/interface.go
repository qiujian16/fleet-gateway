package search

import (
	"context"

	"github.com/qiujian16/fleet-gateway/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type Client interface {
	List(ctx context.Context, gvr schema.GroupVersionResource, listOptions metav1.ListOptions) (runtime.Object, error)

	GetResourceCluster(ctx context.Context, gvr schema.GroupVersionResource, name string) (string, error)

	Resources() []api.ResourceInfo

	ResoursesFor(name string) api.ResourceInfo
}
