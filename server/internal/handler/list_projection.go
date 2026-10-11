package handler

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/multica-ai/multica/server/internal/listshape"
)

// projectList selects only public response fields. Its input is the already
// authorized and redacted response, never a database row. Reflection avoids
// encoding large prompts/configs merely to discard them afterwards.
func projectList[T any](r *http.Request, rows []T, compact []string) (any, error) {
	fields, err := listshape.Fields(r.URL.Query(), compact)
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return rows, nil
	}
	typ := reflect.TypeOf((*T)(nil)).Elem()
	indices := map[string]int{}
	omitEmpty := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")
		if tag[0] == "" || tag[0] == "-" {
			continue
		}
		indices[tag[0]] = i
		omitEmpty[tag[0]] = len(tag) > 1 && tag[1] == "omitempty"
	}
	for _, name := range fields {
		if _, ok := indices[name]; !ok {
			return nil, fmt.Errorf("invalid fields value %q", name)
		}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		v := reflect.ValueOf(row)
		item := map[string]any{}
		for _, name := range fields {
			value := v.Field(indices[name])
			if omitEmpty[name] && (value.IsZero() || ((value.Kind() == reflect.Slice || value.Kind() == reflect.Map) && value.Len() == 0)) {
				continue
			}
			item[name] = value.Interface()
		}
		out = append(out, item)
	}
	return out, nil
}

func writeProjectedList[T any](w http.ResponseWriter, r *http.Request, rows []T, compact []string) {
	out, err := projectList(r, rows, compact)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
