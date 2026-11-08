package shell

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantCmd string
		wantTO  time.Duration
		wantErr string
	}{
		{"默认超时", `{"command":"ls"}`, "ls", DefaultTimeout, ""},
		{"自定义超时", `{"command":"ls","timeout_seconds":30}`, "ls", 30 * time.Second, ""},
		{"超过上限取上限", `{"command":"ls","timeout_seconds":99999}`, "ls", MaxTimeout, ""},
		{"负数取默认", `{"command":"ls","timeout_seconds":-5}`, "ls", DefaultTimeout, ""},
		{"空命令", `{"command":"  "}`, "", 0, "不能为空"},
		{"缺 command", `{}`, "", 0, "不能为空"},
		{"非法 JSON", `{"command":`, "", 0, "JSON"},
		{"空参数", ``, "", 0, "缺少参数"},
		{"类型错误", `{"command":123}`, "", 0, "JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, to, err := ParseArgs(json.RawMessage(c.raw))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("期望错误含 %q，得到 %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if a.Command != c.wantCmd || to != c.wantTO {
				t.Fatalf("得到 %q / %v，期望 %q / %v", a.Command, to, c.wantCmd, c.wantTO)
			}
		})
	}
}

func TestSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(Schema, &v); err != nil {
		t.Fatalf("Schema 不是合法 JSON: %v", err)
	}
	if v["type"] != "object" {
		t.Errorf("Schema.type = %v", v["type"])
	}
}
