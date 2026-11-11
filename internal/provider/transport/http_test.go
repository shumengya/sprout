package transport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeBodyMergesExtra(t *testing.T) {
	body := map[string]any{"model": "m", "thinking": map[string]string{"type": "enabled"}}

	// 没有额外字段时与直接编码逐字节一致。
	plain, err := encodeBody(body, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(body)
	if string(plain) != string(want) {
		t.Errorf("无额外字段 = %s，想要 %s", plain, want)
	}

	// 同名字段被覆盖，新字段追加，原有字段保留。
	got, err := encodeBody(body, map[string]json.RawMessage{
		"thinking": json.RawMessage(`{"type":"disabled"}`),
		"extra":    json.RawMessage(`1`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"extra":1,"model":"m","thinking":{"type":"disabled"}}` {
		t.Errorf("合并结果 = %s", got)
	}

	if _, err := encodeBody(body, map[string]json.RawMessage{"x": json.RawMessage(`{`)}); err == nil || !strings.Contains(err.Error(), "x") {
		t.Errorf("非法 JSON 应报错并指出字段名，得到 %v", err)
	}
}
