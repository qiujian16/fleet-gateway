package api

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
)

type ResourceInfo struct {
	GVR          schema.GroupVersionResource
	Singular     string
	ListKind     string
	Kind         string
	Scope        apiextensionsv1.ResourceScope
	Versions     []string
	SubResources sets.Set[string]
}
