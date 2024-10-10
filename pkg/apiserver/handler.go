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
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiujian16/fleet-gateway/pkg/client/proxy"
	"github.com/qiujian16/fleet-gateway/pkg/client/search"
	fleetresource "github.com/qiujian16/fleet-gateway/pkg/registry/resource"
	"github.com/qiujian16/fleet-gateway/pkg/request"
	"github.com/qiujian16/fleet-gateway/pkg/resourcescheme"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	schemaobjectmeta "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/objectmeta"
	structuralpruning "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/apimachinery/pkg/runtime/serializer/versioning"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	utilwaitgroup "k8s.io/apimachinery/pkg/util/waitgroup"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/metrics"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// resourceHandler serves the `/apis` endpoint.
// This is registered as a filter so that it never collides with any explicitly registered endpoints
type resourceHandler struct {
	versionDiscoveryHandler *versionDiscoveryHandler
	groupDiscoveryHandler   *groupDiscoveryHandler

	customStorageLock sync.Mutex
	// resourceStorage contains a resourceStorageMap
	// atomic.Value has a very good read performance compared to sync.RWMutex
	// see https://gist.github.com/dim/152e6bf80e1384ea72e17ac717a5000a
	// which is suited for most read and rarely write cases
	resourceStorage atomic.Value

	delegate http.Handler

	// so that we can do create on update.
	authorizer authorizer.Authorizer

	// request timeout we should delay storage teardown for
	requestTimeout time.Duration

	// minRequestTimeout applies to CR's list/watch calls
	minRequestTimeout time.Duration

	// staticOpenAPISpec is used as a base for the schema of CR's for the
	// purpose of managing fields, it is how CR handlers get the structure
	// of TypeMeta and ObjectMeta
	staticOpenAPISpec map[string]*spec.Schema

	// The limit on the request size that would be accepted and decoded in a write request
	// 0 means no limit.
	maxRequestBodyBytes int64

	proxyClient proxy.Client
	seachClient search.Client
}

// resourceInfo stores enough information to serve the storage for the custom resource
type resourceInfo struct {
	SubResources string

	// Storage per version
	storage fleetresource.ResourceStorage

	// Request scope per version
	requestScope *handlers.RequestScope

	waitGroup *utilwaitgroup.SafeWaitGroup
}

// resourcesStorageMap goes from resource to its storage
type resourcesStorageMap map[string]*resourceInfo

func NewResourceHandler(
	versionDiscoveryHandler *versionDiscoveryHandler,
	groupDiscoveryHandler *groupDiscoveryHandler,
	delegate http.Handler,
	authorizer authorizer.Authorizer,
	proxyClient proxy.Client,
	searchClient search.Client,
	requestTimeout time.Duration,
	minRequestTimeout time.Duration,
	maxRequestBodyBytes int64) (*resourceHandler, error) {
	ret := &resourceHandler{
		versionDiscoveryHandler: versionDiscoveryHandler,
		groupDiscoveryHandler:   groupDiscoveryHandler,
		resourceStorage:         atomic.Value{},
		delegate:                delegate,
		authorizer:              authorizer,
		requestTimeout:          requestTimeout,
		minRequestTimeout:       minRequestTimeout,
		maxRequestBodyBytes:     maxRequestBodyBytes,
		proxyClient:             proxyClient,
		seachClient:             searchClient,
	}

	ret.resourceStorage.Store(resourcesStorageMap{})

	return ret, nil
}

// watches are expected to handle storage disruption gracefully,
// both on the server-side (by terminating the watch connection)
// and on the client side (by restarting the watch)
var longRunningFilter = genericfilters.BasicLongRunningRequestCheck(sets.NewString("watch"), sets.NewString())

// possiblyAcrossAllNamespacesVerbs contains those verbs which can be per-namespace and across all
// namespaces for namespaces resources. I.e. for these an empty namespace in the requestInfo is fine.
var possiblyAcrossAllNamespacesVerbs = sets.NewString("list", "watch")

func (r *resourceHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path, newURL, err := ClusterPathFromAndStrip(req)
	if err != nil {
		responsewriters.ErrorNegotiated(
			apierrors.NewBadRequest(err.Error()),
			Codecs, schema.GroupVersion{}, w, req,
		)
		return
	}

	var cluster request.Cluster
	if len(path) == 0 {
		// HACK: just a workaround for testing
		cluster.Wildcard = true
	} else {
		err := r.proxyClient.Proxy(path, newURL, w, req)
		if err != nil {
			responsewriters.ErrorNegotiated(
				apierrors.NewBadRequest(err.Error()),
				Codecs, schema.GroupVersion{}, w, req,
			)
			return
		}
		return
	}

	requestInfo, ok := apirequest.RequestInfoFrom(req.Context())
	if !ok {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("no RequestInfo found in the context")),
			Codecs, schema.GroupVersion{}, w, req,
		)
		return
	}
	if !requestInfo.IsResourceRequest {
		pathParts := splitPath(requestInfo.Path)
		// only match /apis/<group>/<version>
		// only registered under /apis
		if len(pathParts) == 3 {
			r.versionDiscoveryHandler.ServeHTTP(w, req)
			return
		}
		// only match /apis/<group>
		if len(pathParts) == 2 {
			r.groupDiscoveryHandler.ServeHTTP(w, req)
			return
		}

		r.delegate.ServeHTTP(w, req)
		return
	}

	info, err := r.requestInfo(requestInfo)
	if err != nil {
		utilruntime.HandleError(err)
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("error resolving resource")),
			Codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
		return
	}

	verb := strings.ToUpper(requestInfo.Verb)
	resource := requestInfo.Resource
	subresource := requestInfo.Subresource
	scope := metrics.CleanScope(requestInfo)
	supportedTypes := []string{
		string(types.JSONPatchType),
		string(types.MergePatchType),
		string(types.ApplyPatchType),
	}

	var handlerFunc http.HandlerFunc
	switch {
	case len(subresource) == 0:
		handlerFunc = r.serveResource(w, req, requestInfo, info, supportedTypes)
	default:
		responsewriters.ErrorNegotiated(
			apierrors.NewNotFound(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Name),
			Codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
	}

	if handlerFunc != nil {
		handlerFunc = metrics.InstrumentHandlerFunc(verb, requestInfo.APIGroup, requestInfo.APIVersion, resource, subresource, scope, metrics.APIServerComponent, false, "", handlerFunc)
		handler := genericfilters.WithWaitGroup(handlerFunc, longRunningFilter, info.waitGroup)
		handler.ServeHTTP(w, req)
		return
	}
}

func (r *resourceHandler) serveResource(w http.ResponseWriter, req *http.Request, requestInfo *apirequest.RequestInfo, info *resourceInfo, supportedTypes []string) http.HandlerFunc {
	requestScope := info.requestScope
	storage := info.storage.Resource

	switch requestInfo.Verb {
	case "list":
		forceWatch := false
		return handlers.ListResource(storage, storage, requestScope, forceWatch, r.minRequestTimeout)
	default:
		responsewriters.ErrorNegotiated(
			apierrors.NewMethodNotSupported(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Verb),
			Codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
		return nil
	}
}

// Wait up to a minute for requests to drain, then tear down storage
func (r *resourceHandler) tearDown(oldInfo *resourceInfo) {
	requestsDrained := make(chan struct{})
	go func() {
		defer close(requestsDrained)
		// Allow time for in-flight requests with a handle to the old info to register themselves
		time.Sleep(time.Second)
	}()

	select {
	case <-time.After(r.requestTimeout * 2):
		klog.Warningf("timeout waiting for requests to drain, tearing down storage")
	case <-requestsDrained:
	}
}

func (r *resourceHandler) requestInfo(requestInfo *apirequest.RequestInfo) (*resourceInfo, error) {
	equivalentResourceRegistry := runtime.NewEquivalentResourceRegistry()

	structuralSchemas := map[string]*structuralschema.Structural{}
	// In addition to Unstructured objects (Custom Resources), we also may sometimes need to
	// decode unversioned Options objects, so we delegate to parameterScheme for such types.
	parameterScheme := runtime.NewScheme()
	parameterScheme.AddUnversionedTypes(schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion},
		&metav1.ListOptions{},
		&metav1.GetOptions{},
		&metav1.DeleteOptions{},
	)
	parameterCodec := runtime.NewParameterCodec(parameterScheme)

	resource := schema.GroupVersionResource{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion, Resource: requestInfo.Resource}
	if len(resource.Resource) == 0 {
		return nil, fmt.Errorf("the server could not properly serve the resource")
	}

	typer := newUnstructuredObjectTyper(parameterScheme)
	creator := unstructuredCreator{}

	gvr := schema.GroupVersionResource{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion, Resource: requestInfo.Resource}
	storage := fleetresource.NewStorage(gvr, r.proxyClient, r.seachClient)

	clusterScoped := len(requestInfo.Namespace) == 0

	// CRDs explicitly do not support protobuf, but some objects returned by the API server do
	negotiatedSerializer := unstructuredNegotiatedSerializer{
		typer:                 typer,
		creator:               creator,
		structuralSchemas:     structuralSchemas,
		preserveUnknownFields: false,
		supportedMediaTypes: []runtime.SerializerInfo{
			{
				MediaType:        "application/json",
				MediaTypeType:    "application",
				MediaTypeSubType: "json",
				EncodesAsText:    true,
				Serializer:       json.NewSerializerWithOptions(json.DefaultMetaFactory, creator, typer, json.SerializerOptions{}),
				PrettySerializer: json.NewSerializerWithOptions(json.DefaultMetaFactory, creator, typer, json.SerializerOptions{Pretty: true}),
				StrictSerializer: json.NewSerializerWithOptions(json.DefaultMetaFactory, creator, typer, json.SerializerOptions{
					Strict: true,
				}),
				StreamSerializer: &runtime.StreamSerializerInfo{
					EncodesAsText: true,
					Serializer:    json.NewSerializerWithOptions(json.DefaultMetaFactory, creator, typer, json.SerializerOptions{}),
					Framer:        json.Framer,
				},
			},
			{
				MediaType:        "application/yaml",
				MediaTypeType:    "application",
				MediaTypeSubType: "yaml",
				EncodesAsText:    true,
				Serializer:       json.NewSerializerWithOptions(json.DefaultMetaFactory, creator, typer, json.SerializerOptions{Yaml: true}),
				StrictSerializer: json.NewSerializerWithOptions(json.DefaultMetaFactory, creator, typer, json.SerializerOptions{
					Yaml:   true,
					Strict: true,
				}),
			},
			{
				MediaType:        "application/vnd.kubernetes.protobuf",
				MediaTypeType:    "application",
				MediaTypeSubType: "vnd.kubernetes.protobuf",
				Serializer:       protobuf.NewSerializer(creator, typer),
				StreamSerializer: &runtime.StreamSerializerInfo{
					Serializer: protobuf.NewRawSerializer(creator, typer),
					Framer:     protobuf.LengthDelimitedFramer,
				},
			},
		},
	}
	var standardSerializers []runtime.SerializerInfo
	for _, s := range negotiatedSerializer.SupportedMediaTypes() {
		if s.MediaType == runtime.ContentTypeProtobuf {
			continue
		}
		standardSerializers = append(standardSerializers, s)
	}

	reqScope := &handlers.RequestScope{
		Namer: handlers.ContextBasedNaming{
			Namer:         meta.NewAccessor(),
			ClusterScoped: clusterScoped,
		},
		Serializer:          negotiatedSerializer,
		ParameterCodec:      parameterCodec,
		StandardSerializers: standardSerializers,

		Creater: creator,
		Typer:   typer,

		EquivalentResourceMapper: equivalentResourceRegistry,

		Resource: schema.GroupVersionResource{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion, Resource: requestInfo.Resource},

		MetaGroupVersion: metav1.SchemeGroupVersion,

		TableConvertor: storage.Resource,

		Authorizer: r.authorizer,

		MaxRequestBodyBytes: r.maxRequestBodyBytes,
	}

	return &resourceInfo{
		requestScope: reqScope,
		storage:      storage,
		SubResources: requestInfo.Subresource,
	}, nil
}

// Destroy shuts down storage layer for all registered CRDs.
// It should be called as a last step of the shutdown sequence.
func (r *resourceHandler) destroy() {
	r.customStorageLock.Lock()
	defer r.customStorageLock.Unlock()
}

type unstructuredNegotiatedSerializer struct {
	typer     runtime.ObjectTyper
	creator   runtime.ObjectCreater
	converter runtime.ObjectConvertor

	structuralSchemas     map[string]*structuralschema.Structural // by version
	structuralSchemaGK    schema.GroupKind
	preserveUnknownFields bool

	supportedMediaTypes []runtime.SerializerInfo
}

func (s unstructuredNegotiatedSerializer) SupportedMediaTypes() []runtime.SerializerInfo {
	return s.supportedMediaTypes
}

func (s unstructuredNegotiatedSerializer) EncoderForVersion(encoder runtime.Encoder, gv runtime.GroupVersioner) runtime.Encoder {
	return versioning.NewCodec(encoder, nil, s.converter, Scheme, Scheme, Scheme, gv, nil, "crdNegotiatedSerializer")
}

func (s unstructuredNegotiatedSerializer) DecoderToVersion(decoder runtime.Decoder, gv runtime.GroupVersioner) runtime.Decoder {
	returnUnknownFieldPaths := false
	if serializer, ok := decoder.(*json.Serializer); ok {
		returnUnknownFieldPaths = serializer.IsStrict()
	}
	d := schemaCoercingDecoder{delegate: decoder, validator: unstructuredSchemaCoercer{structuralSchemas: s.structuralSchemas, structuralSchemaGK: s.structuralSchemaGK, preserveUnknownFields: s.preserveUnknownFields, returnUnknownFieldPaths: returnUnknownFieldPaths}}
	return versioning.NewCodec(nil, d, runtime.UnsafeObjectConvertor(Scheme), Scheme, Scheme, unstructuredDefaulter{
		delegate:           Scheme,
		structuralSchemas:  s.structuralSchemas,
		structuralSchemaGK: s.structuralSchemaGK,
	}, nil, gv, "unstructuredNegotiatedSerializer")
}

type UnstructuredObjectTyper struct {
	Delegate          runtime.ObjectTyper
	UnstructuredTyper runtime.ObjectTyper
}

func newUnstructuredObjectTyper(Delegate runtime.ObjectTyper) UnstructuredObjectTyper {
	return UnstructuredObjectTyper{
		Delegate:          Delegate,
		UnstructuredTyper: resourcescheme.NewUnstructuredObjectTyper(),
	}
}

func (t UnstructuredObjectTyper) ObjectKinds(obj runtime.Object) ([]schema.GroupVersionKind, bool, error) {
	// Delegate for things other than Unstructured.
	if _, ok := obj.(runtime.Unstructured); !ok {
		return t.Delegate.ObjectKinds(obj)
	}
	return t.UnstructuredTyper.ObjectKinds(obj)
}

func (t UnstructuredObjectTyper) Recognizes(gvk schema.GroupVersionKind) bool {
	return t.Delegate.Recognizes(gvk) || t.UnstructuredTyper.Recognizes(gvk)
}

type unstructuredCreator struct{}

func (c unstructuredCreator) New(kind schema.GroupVersionKind) (runtime.Object, error) {
	ret := &unstructured.Unstructured{}
	ret.SetGroupVersionKind(kind)
	return ret, nil
}

type unstructuredDefaulter struct {
	delegate           runtime.ObjectDefaulter
	structuralSchemas  map[string]*structuralschema.Structural // by version
	structuralSchemaGK schema.GroupKind
}

func (d unstructuredDefaulter) Default(in runtime.Object) {
	// Delegate for things other than Unstructured, and other GKs
	u, ok := in.(runtime.Unstructured)
	if !ok || u.GetObjectKind().GroupVersionKind().GroupKind() != d.structuralSchemaGK {
		d.delegate.Default(in)
		return
	}

	structuraldefaulting.Default(u.UnstructuredContent(), d.structuralSchemas[u.GetObjectKind().GroupVersionKind().Version])
}

// clone returns a clone of the provided crdStorageMap.
// The clone is a shallow copy of the map.
func (in resourcesStorageMap) clone() resourcesStorageMap {
	if in == nil {
		return nil
	}
	out := make(resourcesStorageMap, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// schemaCoercingDecoder calls the delegate decoder, and then applies the Unstructured schema validator
// to coerce the schema.
type schemaCoercingDecoder struct {
	delegate  runtime.Decoder
	validator unstructuredSchemaCoercer
}

var _ runtime.Decoder = schemaCoercingDecoder{}

func (d schemaCoercingDecoder) Decode(data []byte, defaults *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	var decodingStrictErrs []error
	obj, gvk, err := d.delegate.Decode(data, defaults, into)
	if err != nil {
		decodeStrictErr, ok := runtime.AsStrictDecodingError(err)
		if !ok || obj == nil {
			return nil, gvk, err
		}
		decodingStrictErrs = decodeStrictErr.Errors()
	}
	var unknownFields []string
	if u, ok := obj.(*unstructured.Unstructured); ok {
		unknownFields, err = d.validator.apply(u)
		if err != nil {
			return nil, gvk, err
		}
	}
	if d.validator.returnUnknownFieldPaths && (len(decodingStrictErrs) > 0 || len(unknownFields) > 0) {
		for _, unknownField := range unknownFields {
			decodingStrictErrs = append(decodingStrictErrs, fmt.Errorf(`unknown field "%s"`, unknownField))
		}
		return obj, gvk, runtime.NewStrictDecodingError(decodingStrictErrs)
	}

	return obj, gvk, nil
}

// schemaCoercingConverter calls the delegate converter and applies the Unstructured validator to
// coerce the schema.
type schemaCoercingConverter struct {
	delegate  runtime.ObjectConvertor
	validator unstructuredSchemaCoercer
}

var _ runtime.ObjectConvertor = schemaCoercingConverter{}

func (v schemaCoercingConverter) Convert(in, out, context interface{}) error {
	if err := v.delegate.Convert(in, out, context); err != nil {
		return err
	}

	if u, ok := out.(*unstructured.Unstructured); ok {
		if _, err := v.validator.apply(u); err != nil {
			return err
		}
	}

	return nil
}

func (v schemaCoercingConverter) ConvertToVersion(in runtime.Object, gv runtime.GroupVersioner) (runtime.Object, error) {
	out, err := v.delegate.ConvertToVersion(in, gv)
	if err != nil {
		return nil, err
	}

	if u, ok := out.(*unstructured.Unstructured); ok {
		if _, err := v.validator.apply(u); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (v schemaCoercingConverter) ConvertFieldLabel(gvk schema.GroupVersionKind, label, value string) (string, string, error) {
	return v.delegate.ConvertFieldLabel(gvk, label, value)
}

// unstructuredSchemaCoercer adds to unstructured unmarshalling what json.Unmarshal does
// in addition for native types when decoding into Golang structs:
//
// - validating and pruning ObjectMeta
// - generic pruning of unknown fields following a structural schema
// - removal of non-defaulted non-nullable null map values.
type unstructuredSchemaCoercer struct {
	dropInvalidMetadata bool
	repairGeneration    bool

	structuralSchemas       map[string]*structuralschema.Structural
	structuralSchemaGK      schema.GroupKind
	preserveUnknownFields   bool
	returnUnknownFieldPaths bool
}

func (v *unstructuredSchemaCoercer) apply(u *unstructured.Unstructured) (unknownFieldPaths []string, err error) {
	// save implicit meta fields that don't have to be specified in the validation spec
	kind, foundKind, err := unstructured.NestedString(u.UnstructuredContent(), "kind")
	if err != nil {
		return nil, err
	}
	apiVersion, foundApiVersion, err := unstructured.NestedString(u.UnstructuredContent(), "apiVersion")
	if err != nil {
		return nil, err
	}
	objectMeta, foundObjectMeta, metaUnknownFields, err := schemaobjectmeta.GetObjectMetaWithOptions(u.Object, schemaobjectmeta.ObjectMetaOptions{
		DropMalformedFields:     v.dropInvalidMetadata,
		ReturnUnknownFieldPaths: v.returnUnknownFieldPaths,
	})
	if err != nil {
		return nil, err
	}
	unknownFieldPaths = append(unknownFieldPaths, metaUnknownFields...)

	// compare group and kind because also other object like DeleteCollection options pass through here
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, err
	}

	if gv.Group == v.structuralSchemaGK.Group && kind == v.structuralSchemaGK.Kind {
		if !v.preserveUnknownFields {
			// TODO: switch over pruning and coercing at the root to schemaobjectmeta.Coerce too
			pruneOpts := structuralschema.UnknownFieldPathOptions{}
			if v.returnUnknownFieldPaths {
				pruneOpts.TrackUnknownFieldPaths = true
			}
			unknownFieldPaths = append(unknownFieldPaths, structuralpruning.PruneWithOptions(u.Object, v.structuralSchemas[gv.Version], true, pruneOpts)...)
			structuraldefaulting.PruneNonNullableNullsWithoutDefaults(u.Object, v.structuralSchemas[gv.Version])
		}

		err, paths := schemaobjectmeta.CoerceWithOptions(nil, u.Object, v.structuralSchemas[gv.Version], false, schemaobjectmeta.CoerceOptions{
			DropInvalidFields:       v.dropInvalidMetadata,
			ReturnUnknownFieldPaths: v.returnUnknownFieldPaths,
		})
		if err != nil {
			return nil, err
		}
		unknownFieldPaths = append(unknownFieldPaths, paths...)

		// fixup missing generation in very old CRs
		if v.repairGeneration && objectMeta.Generation == 0 {
			objectMeta.Generation = 1
		}
	}

	// restore meta fields, starting clean
	if foundKind {
		u.SetKind(kind)
	}
	if foundApiVersion {
		u.SetAPIVersion(apiVersion)
	}
	if foundObjectMeta {
		if err := schemaobjectmeta.SetObjectMeta(u.Object, objectMeta); err != nil {
			return nil, err
		}
	}

	return unknownFieldPaths, nil
}

// ClusterPathFromAndStrip parses the request for a logical cluster path, returns it if found
// and strips it from the request URL path.
func ClusterPathFromAndStrip(req *http.Request) (string, string, error) {
	if reqPath := req.URL.Path; strings.HasPrefix(reqPath, "/clusters/") {
		reqPath = strings.TrimPrefix(reqPath, "/clusters/")

		i := strings.Index(reqPath, "/")
		if i == -1 {
			reqPath += "/"
			i = len(reqPath) - 1
		}
		return reqPath[:i], reqPath[i:], nil
	}

	return "", req.URL.Path, nil
}
