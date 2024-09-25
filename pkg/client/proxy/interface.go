package proxy

import (
	"k8s.io/client-go/dynamic"
)

type Client interface {
	DynamicClient(cluster string) (dynamic.Interface, error)
}
