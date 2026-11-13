package relay

import (
	"encoding/json"
	"errors"
)

var errModelRequired = errors.New("model is required")

// requestBody 是客户端请求体在整条转发链路里的唯一载体：只解析一次，
// 参数覆盖与 model 替换都落在同一个 map 上，避免每层各自 unmarshal/marshal。
// stream 不单独存字段，始终从 fields 读取，保证发给上游的 body 与
// provider 的 Stream 开关来自同一个真相源。
type requestBody struct {
	model  string // 客户端原始模型名，不随 overrides 改变
	fields map[string]any
}

func parseRequestBody(raw []byte) (*requestBody, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	model, _ := fields["model"].(string)
	if model == "" {
		return nil, errModelRequired
	}
	return &requestBody{model: model, fields: fields}, nil
}

func (b *requestBody) isStream() bool {
	v, _ := b.fields["stream"].(bool)
	return v
}

// applyOverrides 把映射侧的强制参数覆盖进请求体；model 字段不受影响，
// 始终由 bytesFor 在发出前替换为模型实体名。
func (b *requestBody) applyOverrides(overrides map[string]any) {
	for k, v := range overrides {
		b.fields[k] = v
	}
}

func (b *requestBody) bytesFor(model string) ([]byte, error) {
	b.fields["model"] = model
	return json.Marshal(b.fields)
}
