package admin

import (
	"errors"
	"net/http"
	"strings"

	"pengapi/internal/httpx"
	"pengapi/internal/store"
)

type mappingInput struct {
	Name               string         `json:"name"`
	ContextLength      *int64         `json:"context_length"`
	MaxOutputTokens    *int64         `json:"max_output_tokens"`
	ModelIDs           []int64        `json:"model_ids"`
	ParameterOverrides map[string]any `json:"parameter_overrides"`
}

const maxParameterOverrides = 100

// normalizeOverrides 校验并归一化强制覆盖参数：键去空白、不能为空；返回 nil 表示无覆盖
func normalizeOverrides(in map[string]any) (map[string]any, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxParameterOverrides {
		return nil, errors.New("too many parameter overrides")
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, errors.New("override parameter name is required")
		}
		out[k] = v
	}
	return out, nil
}

func (h dataAPI) listMappings(w http.ResponseWriter, r *http.Request) {
	mappings, err := h.store.ListMappings(r.Context())
	if err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": mappings})
}

func (h dataAPI) createMapping(w http.ResponseWriter, r *http.Request) {
	var in mappingInput
	if !httpx.DecodeAdminJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	overrides, err := normalizeOverrides(in.ParameterOverrides)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	mp, err := h.store.CreateMapping(r.Context(), store.MappingInput{
		Name: in.Name, ContextLength: in.ContextLength, MaxOutputTokens: in.MaxOutputTokens,
		ModelIDs: in.ModelIDs, ParameterOverrides: overrides,
	})
	if err != nil {
		writeMappingErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, mp)
}

// updateMapping 全量更新（含绑定 model_ids 整体替换）
func (h dataAPI) updateMapping(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var in mappingInput
	if !httpx.DecodeAdminJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		httpx.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	overrides, err := normalizeOverrides(in.ParameterOverrides)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	err = h.store.UpdateMapping(r.Context(), id, store.MappingInput{
		Name: strings.TrimSpace(in.Name), ContextLength: in.ContextLength,
		MaxOutputTokens: in.MaxOutputTokens, ModelIDs: in.ModelIDs, ParameterOverrides: overrides,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "mapping not found")
			return
		}
		writeMappingErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h dataAPI) deleteMapping(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteMapping(r.Context(), id); err != nil {
		httpx.InternalError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// writeMappingErr 把 store 的领域错误翻译成管理端状态码。
func writeMappingErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		httpx.Error(w, http.StatusBadRequest, "name already exists")
	case errors.Is(err, store.ErrInvalidReference):
		httpx.Error(w, http.StatusBadRequest, "invalid model_ids")
	default:
		httpx.InternalError(w, err)
	}
}
