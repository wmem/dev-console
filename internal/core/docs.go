package core

import (
	"devconsole/internal/auth"
	c "devconsole/internal/contracts"
	"encoding/json"
	"fmt"
	"strings"
)

func (k *Core) Document(key string, p auth.Principal, base string) (string, map[string]any, error) {
	project, e := k.Project(key, p, "docs:read")
	if e != nil {
		return "", nil, e
	}
	bs, e := k.Bindings(project.ID, false)
	if e != nil {
		return "", nil, e
	}
	paths := map[string]any{}
	var text strings.Builder
	fmt.Fprintf(&text, "# %s\n\n项目：`%s` · 文档版本：%d · 更新时间：%s\n\n用途：%s\n\n操作流程：%s\n\n注意事项：%s\n\n", escape(project.DisplayName), project.Key, project.DocRevision, project.UpdatedAt.Format("2006-01-02T15:04:05Z"), escape(project.Purpose), escape(project.UsageInstructions), escape(project.Notes))
	text.WriteString("## 调用约定\n\n通过 Authorization: Bearer <ACCESS_TOKEN> 鉴权。通信冲突立即返回 RESOURCE_BUSY（409），不排队、不自动重试。控制超时可能为 OUTCOME_UNKNOWN，不表示动作未发生，需由调用方检查状态。设置成功表示设定或输出状态回读一致，不表示物理输出稳定精度已验证。observed_at 使用 UTC RFC3339；source=live 表示实时采集，旧值不得冒充实时成功。\n\n错误：400 INVALID_ARGUMENT；401 UNAUTHENTICATED；403 FORBIDDEN / PROJECT_DISABLED / SERVICE_DISABLED；404 SERVICE_NOT_FOUND；409 RESOURCE_BUSY / SERVICE_UNBOUND / BINDING_INVALID；502 DEVICE_ERROR / PROTOCOL_ERROR / VERIFICATION_FAILED；503 DEVICE_OFFLINE；504 TIMEOUT / OUTCOME_UNKNOWN。错误 outcome 按事实为 not_started、unknown 或 partially_completed。\n\n")
	for _, b := range bs {
		available := b.Enabled && b.BindingState == "valid"
		if !available && p.Kind == "token" {
			continue
		}
		fmt.Fprintf(&text, "## %s（%s）\n\n%s\n\n使用说明：%s\n\n", escape(b.DisplayName), b.Key, escape(b.Description), escape(b.UsageInstructions))
		if !available {
			fmt.Fprintf(&text, "当前不可调用：enabled=%t，binding=%s。\n\n", b.Enabled, b.BindingState)
			continue
		}
		path := "/api/v1/projects/" + project.Key + "/services/" + b.Key + "/invoke"
		fmt.Fprintf(&text, "类型：%s；契约：%s / %d\n\nPOST `%s`\n\n| 参数 | 类型 | 单位 | 下限 | 上限 | 精度 | 默认值 | 必填 |\n|---|---|---|---|---|---|---|---|\n", b.Contract.Kind, b.Contract.Contract, b.Contract.ContractVersion, path)
		input := b.Contract.Input
		requestRequired := []string{}
		example := map[string]any{}
		for _, key := range c.Keys(input.Properties) {
			s := input.Properties[key]
			d, has := b.DefaultParameters[key]
			if has {
				s.Default = d
				input.Properties[key] = s
			}
			required := false
			for _, r := range input.Required {
				if r == key {
					required = true
				}
			}
			if has {
				required = false
			}
			if required {
				requestRequired = append(requestRequired, key)
			}
			fmt.Fprintf(&text, "| %s | %s | %s | %s | %s | %s | %s | %t |\n", key, s.Type, escape(s.Unit), num(s.Minimum), num(s.Maximum), num(s.MultipleOf), jstring(d), required)
			if has {
				example[key] = d
			} else if len(s.Enum) > 0 {
				example[key] = s.Enum[0]
			} else {
				switch s.Type {
				case "boolean":
					example[key] = false
				case "number", "integer":
					v := 0.0
					if s.Minimum != nil {
						v = *s.Minimum
					}
					example[key] = v
				case "string":
					example[key] = ""
				}
			}
		}
		input.Required = requestRequired
		body := map[string]any{"parameters": example}
		data, _ := json.Marshal(body)
		fmt.Fprintf(&text, "\n请求示例（说明用途，不代表已执行）：\n\n```json\n%s\n```\n\n```sh\ncurl -X POST %s \\\n  -H 'Authorization: Bearer <ACCESS_TOKEN>' \\\n  -H 'Content-Type: application/json' \\\n  --data %s\n```\n\n输出契约：\n\n```json\n%s\n```\n\n", pretty(body), quote(strings.TrimRight(base, "/")+path), quote(string(data)), pretty(b.Contract.Output))
		responseExample := map[string]any{"request_id": "<REQUEST_ID>", "status": "succeeded", "result": outputExample(b.Contract.Output, example), "confirmation": "read", "observed_at": "<UTC_RFC3339>", "source": "live", "duration_ms": 0}
		if b.Contract.Kind == "operation" {
			responseExample["confirmation"] = "verified"
		}
		fmt.Fprintf(&text, "响应结构示例（数值仅示意，不是实测记录）：\n\n```json\n%s\n```\n\n", pretty(responseExample))
		paths[path] = map[string]any{"post": map[string]any{"operationId": project.Key + "_" + b.Key, "summary": b.DisplayName, "description": b.Description + "\n" + b.UsageInstructions, "security": []any{map[string]any{"projectToken": []string{}}}, "requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": c.Object(map[string]c.Schema{"parameters": input}, "parameters"), "example": body}}}, "responses": map[string]any{"200": map[string]any{"description": "成功；采集结果带时间和来源，操作回读确认", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"request_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "const": "succeeded"}, "result": b.Contract.Output, "confirmation": map[string]any{"type": "string"}, "observed_at": map[string]any{"type": "string", "format": "date-time"}, "source": map[string]any{"type": "string", "enum": []string{"live", "cache"}}}}}}}, "default": map[string]any{"description": "统一错误；包含request_id、error.code/message/outcome"}}}}
	}
	spec := map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": project.DisplayName, "version": fmt.Sprint(project.DocRevision), "description": project.Purpose}, "servers": []any{map[string]any{"url": strings.TrimRight(base, "/")}}, "paths": paths, "components": map[string]any{"securitySchemes": map[string]any{"projectToken": map[string]any{"type": "http", "scheme": "bearer"}}}}
	return text.String(), spec, nil
}
func outputExample(s c.Schema, input map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range s.Required {
		p := s.Properties[key]
		name := strings.TrimPrefix(strings.TrimPrefix(key, "requested_"), "observed_")
		if v, ok := input[name]; ok {
			out[key] = v
			continue
		}
		switch p.Type {
		case "boolean":
			out[key] = false
		case "number", "integer":
			out[key] = 0.0
		case "string":
			out[key] = "<STRING>"
		}
	}
	return out
}
func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "`", "\\`", "|", "\\|", "\n", "  \n")
	return r.Replace(s)
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func num(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%g", *v)
}
func pretty(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) }
func jstring(v any) string {
	if v == nil {
		return "—"
	}
	b, _ := json.Marshal(v)
	return escape(string(b))
}
