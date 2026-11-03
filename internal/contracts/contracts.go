package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Outcome string `json:"outcome"`
}

func (e *Error) Error() string    { return e.Message }
func Fail(code, msg string) error { return &Error{code, msg, "not_started"} }
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{"INTERNAL_ERROR", "内部处理失败", "unknown"}
}

type Schema struct {
	Type                 string            `json:"type"`
	Properties           map[string]Schema `json:"properties,omitempty"`
	Required             []string          `json:"required,omitempty"`
	AdditionalProperties bool              `json:"additionalProperties"`
	Description          string            `json:"description,omitempty"`
	Unit                 string            `json:"unit,omitempty"`
	Minimum              *float64          `json:"minimum,omitempty"`
	Maximum              *float64          `json:"maximum,omitempty"`
	MultipleOf           *float64          `json:"multipleOf,omitempty"`
	Enum                 []any             `json:"enum,omitempty"`
	Default              any               `json:"default,omitempty"`
}

func Num(min, max, step float64, unit string) Schema {
	return Schema{Type: "number", Minimum: &min, Maximum: &max, MultipleOf: &step, Unit: unit}
}
func Object(props map[string]Schema, required ...string) Schema {
	return Schema{Type: "object", Properties: props, Required: required}
}
func (s Schema) Validate(v any) error {
	bad := func(msg string) error { return Fail("INVALID_ARGUMENT", msg) }
	if len(s.Enum) > 0 {
		found := false
		a, _ := json.Marshal(v)
		for _, x := range s.Enum {
			b, _ := json.Marshal(x)
			if string(a) == string(b) {
				found = true
			}
		}
		if !found {
			return bad("参数不在允许值中")
		}
	}
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return bad("参数必须是对象")
		}
		for _, k := range s.Required {
			if _, ok := m[k]; !ok {
				return bad("缺少参数：" + k)
			}
		}
		for k, x := range m {
			p, ok := s.Properties[k]
			if !ok {
				if !s.AdditionalProperties {
					return bad("未知参数：" + k)
				}
				continue
			}
			if e := p.Validate(x); e != nil {
				return bad(k + "：" + e.Error())
			}
		}
	case "number", "integer":
		n, ok := v.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return bad("必须是有限数值")
		}
		if s.Type == "integer" && math.Trunc(n) != n {
			return bad("必须是整数")
		}
		if s.Minimum != nil && n < *s.Minimum {
			return bad("小于允许下限")
		}
		if s.Maximum != nil && n > *s.Maximum {
			return bad("超过允许上限")
		}
		if s.MultipleOf != nil {
			if *s.MultipleOf <= 0 {
				return bad("精度约束非法")
			}
			q := n / *s.MultipleOf
			if math.Abs(q-math.Round(q)) > 1e-8*math.Max(1, math.Abs(q)) {
				return bad("不符合设备精度")
			}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return bad("必须是布尔值")
		}
	case "string":
		if _, ok := v.(string); !ok {
			return bad("必须是字符串")
		}
	default:
		return bad("不支持的Schema类型：" + s.Type)
	}
	return nil
}
func (s Schema) Parameters(p, defaults map[string]any) (map[string]any, error) {
	m := map[string]any{}
	for k, v := range defaults {
		m[k] = v
	}
	for k, v := range p {
		m[k] = v
	}
	return m, s.Validate(m)
}

// Tighten只允许项目约束收紧数值范围、精度和枚举，不允许修改结构或必填字段。
func (s Schema) Tighten(c map[string]Schema) (Schema, error) {
	b, _ := json.Marshal(s)
	var out Schema
	_ = json.Unmarshal(b, &out)
	for k, v := range c {
		p, ok := out.Properties[k]
		if !ok {
			return out, Fail("INVALID_ARGUMENT", "约束引用未知参数："+k)
		}
		if v.Type != "" && v.Type != p.Type {
			return out, Fail("INVALID_ARGUMENT", "不能修改参数类型")
		}
		if len(v.Properties) > 0 || len(v.Required) > 0 || v.AdditionalProperties || v.Default != nil {
			return out, Fail("INVALID_ARGUMENT", "不能修改参数结构或默认值")
		}
		if v.Minimum != nil {
			if p.Minimum != nil && *v.Minimum < *p.Minimum {
				return out, Fail("INVALID_ARGUMENT", "不能放宽下限")
			}
			p.Minimum = v.Minimum
		}
		if v.Maximum != nil {
			if p.Maximum != nil && *v.Maximum > *p.Maximum {
				return out, Fail("INVALID_ARGUMENT", "不能放宽上限")
			}
			p.Maximum = v.Maximum
		}
		if p.Minimum != nil && p.Maximum != nil && *p.Minimum > *p.Maximum {
			return out, Fail("INVALID_ARGUMENT", "参数范围为空")
		}
		if v.MultipleOf != nil {
			if *v.MultipleOf <= 0 {
				return out, Fail("INVALID_ARGUMENT", "精度必须为正数")
			}
			if p.MultipleOf != nil {
				q := *v.MultipleOf / *p.MultipleOf
				if math.Abs(q-math.Round(q)) > 1e-8 {
					return out, Fail("INVALID_ARGUMENT", "不能放宽或错开精度")
				}
			}
			p.MultipleOf = v.MultipleOf
		}
		if len(v.Enum) > 0 {
			for _, x := range v.Enum {
				if e := p.Validate(x); e != nil {
					return out, e
				}
			}
			p.Enum = v.Enum
		}
		out.Properties[k] = p
	}
	return out, nil
}
func Keys[T any](m map[string]T) []string {
	a := make([]string, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	sort.Strings(a)
	return a
}

type Service struct {
	Key                  string `json:"key"`
	Contract             string `json:"contract"`
	ContractVersion      int    `json:"contract_version"`
	Title                string `json:"title"`
	Description          string `json:"description"`
	Kind                 string `json:"kind"`
	Input                Schema `json:"input_schema"`
	Output               Schema `json:"output_schema"`
	TimeoutMS            int    `json:"timeout_ms"`
	SupportsVerification bool   `json:"supports_verification"`
}
type Device struct {
	TemplateKey  string    `json:"template_key"`
	ID           string    `json:"id"`
	DisplayName  string    `json:"display_name"`
	AccessRoute  string    `json:"access_route"`
	Availability string    `json:"availability"`
	LastError    string    `json:"last_error,omitempty"`
	Services     []Service `json:"services"`
	Revision     int       `json:"revision"`
}
type Template struct {
	Key             string `json:"key"`
	Title           string `json:"title"`
	ConfigSchema    Schema `json:"config_schema"`
	ConnectionField string `json:"connection_field,omitempty"`
}
type ConnectionChoice struct {
	Value        string `json:"value"`
	Label        string `json:"label"`
	Mode         string `json:"mode"`
	ResolvedPath string `json:"resolved_path"`
	Selectable   bool   `json:"selectable"`
	Warning      string `json:"warning,omitempty"`
}
type Invocation struct {
	RequestID  string         `json:"request_id"`
	DeviceID   string         `json:"device_id"`
	ServiceKey string         `json:"device_service_key"`
	Parameters map[string]any `json:"parameters"`
	Deadline   time.Time      `json:"deadline"`
}
type Result struct {
	Output       map[string]any `json:"result"`
	Confirmation string         `json:"confirmation"`
	ObservedAt   time.Time      `json:"observed_at"`
	Source       string         `json:"source"`
}
type DeviceRegistrar interface {
	UpsertDevice(context.Context, Device) error
	ReportAvailability(context.Context, string, string, string) error
}
type DeviceInvoker interface {
	Invoke(context.Context, Invocation) (Result, error)
}
type DeviceProvisioner interface {
	ListTemplates(context.Context) []Template
	DiscoverConnections(context.Context, string) ([]ConnectionChoice, error)
	Create(context.Context, string, string, json.RawMessage) (Device, error)
	Update(context.Context, string, string, json.RawMessage) (Device, error)
	Remove(context.Context, string) error
	Test(context.Context, string) error
	Config(context.Context, string) (json.RawMessage, error)
}

func ServiceByKey(d Device, key string) (Service, error) {
	for _, s := range d.Services {
		if s.Key == key {
			return s, nil
		}
	}
	return Service{}, Fail("BINDING_INVALID", fmt.Sprintf("设备服务 %s 不可用", key))
}
