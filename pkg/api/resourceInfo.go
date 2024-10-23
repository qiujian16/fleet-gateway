package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metatable "k8s.io/apimachinery/pkg/api/meta/table"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/jsonpath"
	"reflect"
)

const clusterLabelKey = "open-cluster-management/cluster"

type ResourceInfo struct {
	Name         string
	Singular     string
	ListKind     string
	Kind         string
	Scope        apiextensionsv1.ResourceScope
	SubResources sets.Set[string]
	Converter    *Convertor
}

type ColumnPrinter interface {
	FindResults(data interface{}) ([][]reflect.Value, error)
	PrintResults(w io.Writer, results []reflect.Value) error
}

type Convertor struct {
	Headers           []metav1.TableColumnDefinition
	AdditionalColumns []ColumnPrinter
}

type ResourceColumns struct {
	Name     string
	Type     string
	Format   string
	Priority int32
	JSONPath string
}

func NewConverter(columns []ResourceColumns) *Convertor {
	headers := []metav1.TableColumnDefinition{
		{Name: "Cluster", Type: "string", Format: "name"},
		{Name: "Name", Type: "string", Format: "name"},
	}
	c := &Convertor{
		Headers: headers,
	}

	for _, col := range columns {
		path := jsonpath.New(col.Name)
		if err := path.Parse(fmt.Sprintf("{%s}", col.JSONPath)); err != nil {
			return &Convertor{
				Headers: headers,
			}
		}
		path.AllowMissingKeys(true)

		desc := fmt.Sprintf("Custom resource definition column (in JSONPath format): %s", col.JSONPath)

		c.AdditionalColumns = append(c.AdditionalColumns, path)
		c.Headers = append(c.Headers, metav1.TableColumnDefinition{
			Name:        col.Name,
			Type:        col.Type,
			Format:      col.Format,
			Description: desc,
			Priority:    col.Priority,
		})
	}

	return c
}

func (c *Convertor) ConvertToTable(ctx context.Context, obj runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{}
	opt, ok := tableOptions.(*metav1.TableOptions)
	noHeaders := ok && opt != nil && opt.NoHeaders
	if !noHeaders {
		table.ColumnDefinitions = c.Headers
	}

	if m, err := meta.ListAccessor(obj); err == nil {
		table.ResourceVersion = m.GetResourceVersion()
		table.Continue = m.GetContinue()
		table.RemainingItemCount = m.GetRemainingItemCount()
	} else {
		if m, err := meta.CommonAccessor(obj); err == nil {
			table.ResourceVersion = m.GetResourceVersion()
		}
	}

	var err error
	buf := &bytes.Buffer{}
	table.Rows, err = metatable.MetaToTableRow(obj, func(obj runtime.Object, m metav1.Object, name, age string) ([]interface{}, error) {
		clusterName, err := getClusterFromMeta(obj)
		if err != nil {
			return nil, err
		}
		cells := make([]interface{}, 2, 2+len(c.AdditionalColumns))
		cells[0] = clusterName
		cells[1] = name
		customHeaders := c.Headers[2:]
		accessor, err := meta.Accessor(obj)
		if err != nil {
			return nil, err
		}
		annotations := accessor.GetAnnotations()
		var content map[string]interface{}
		if val, ok := annotations["rawdata"]; ok {
			err := json.Unmarshal([]byte(val), &content)
			if err != nil {
				return nil, err
			}
		}
		for i, column := range c.AdditionalColumns {
			results, err := column.FindResults(content)
			if err != nil || len(results) == 0 || len(results[0]) == 0 {
				cells = append(cells, nil)
				continue
			}

			// as we only support simple JSON path, we can assume to have only one result (or none, filtered out above)
			value := results[0][0].Interface()
			if customHeaders[i].Type == "string" {
				if err := column.PrintResults(buf, []reflect.Value{reflect.ValueOf(value)}); err == nil {
					cells = append(cells, buf.String())
					buf.Reset()
				} else {
					cells = append(cells, nil)
				}
			} else {
				cells = append(cells, cellForJSONValue(customHeaders[i].Type, value))
			}
		}
		return cells, nil
	})
	return table, err
}

func cellForJSONValue(headerType string, value interface{}) interface{} {
	if value == nil {
		return nil
	}

	switch headerType {
	case "integer":
		switch typed := value.(type) {
		case int64:
			return typed
		case float64:
			return int64(typed)
		case json.Number:
			if i64, err := typed.Int64(); err == nil {
				return i64
			}
		}
	case "number":
		switch typed := value.(type) {
		case int64:
			return float64(typed)
		case float64:
			return typed
		case json.Number:
			if f, err := typed.Float64(); err == nil {
				return f
			}
		}
	case "boolean":
		if b, ok := value.(bool); ok {
			return b
		}
	case "string":
		if s, ok := value.(string); ok {
			return s
		}
	case "date":
		if typed, ok := value.(string); ok {
			var timestamp metav1.Time
			err := timestamp.UnmarshalQueryParameter(typed)
			if err != nil {
				return "<invalid>"
			}
			return metatable.ConvertToHumanReadableDateType(timestamp)
		}
	}

	return nil
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
