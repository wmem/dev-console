package core

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"devconsole/internal/auth"
	c "devconsole/internal/contracts"
	"devconsole/internal/storage"
)

type Project struct {
	ID                string    `json:"id"`
	Key               string    `json:"key"`
	DisplayName       string    `json:"display_name"`
	Purpose           string    `json:"purpose"`
	UsageInstructions string    `json:"usage_instructions"`
	Notes             string    `json:"notes"`
	Enabled           bool      `json:"enabled"`
	Revision          int       `json:"revision"`
	DocRevision       int       `json:"doc_revision"`
	UpdatedAt         time.Time `json:"updated_at"`
	MemberIDs         []string  `json:"member_ids"`
	Deleted           bool      `json:"deleted,omitempty"`
}
type Binding struct {
	ID                string              `json:"id"`
	ProjectID         string              `json:"project_id"`
	Key               string              `json:"key"`
	DisplayName       string              `json:"display_name"`
	Description       string              `json:"description"`
	UsageInstructions string              `json:"usage_instructions"`
	DeviceID          string              `json:"device_id"`
	DeviceServiceKey  string              `json:"device_service_key"`
	Contract          c.Service           `json:"service_contract"`
	DefaultParameters map[string]any      `json:"default_parameters"`
	Constraints       map[string]c.Schema `json:"parameter_constraints"`
	Enabled           bool                `json:"enabled"`
	Revision          int                 `json:"revision"`
	BindingState      string              `json:"binding_state"`
	Deleted           bool                `json:"deleted,omitempty"`
}
type Record struct {
	RequestID        string         `json:"request_id"`
	SubjectID        string         `json:"subject_id"`
	SubjectKind      string         `json:"subject_kind"`
	ProjectID        string         `json:"project_id,omitempty"`
	ProjectKey       string         `json:"project,omitempty"`
	ServiceKey       string         `json:"service"`
	ServiceRevision  int            `json:"service_revision"`
	DeviceID         string         `json:"device_id"`
	DeviceServiceKey string         `json:"device_service_key"`
	Parameters       map[string]any `json:"parameters"`
	Status           string         `json:"status"`
	Result           map[string]any `json:"result,omitempty"`
	Confirmation     string         `json:"confirmation,omitempty"`
	Source           string         `json:"source,omitempty"`
	ObservedAt       *time.Time     `json:"observed_at,omitempty"`
	Error            *c.Error       `json:"error,omitempty"`
	StartedAt        time.Time      `json:"started_at"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	DurationMS       int64          `json:"duration_ms"`
}
type Core struct {
	Store       *storage.Store
	Invoker     c.DeviceInvoker
	Provisioner c.DeviceProvisioner
	mu          sync.Mutex
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func New(s *storage.Store) *Core { return &Core{Store: s} }
func (k *Core) UpsertDevice(ctx context.Context, d c.Device) error {
	var old c.Device
	changed := false
	if k.Store.Get("devices", d.ID, &old) == nil {
		d.DisplayName = old.DisplayName
		d.Revision = old.Revision
		before, _ := json.Marshal(old.Services)
		after, _ := json.Marshal(d.Services)
		changed = string(before) != string(after)
	}
	if d.Revision == 0 {
		d.Revision = 1
	}
	changes := []storage.Change{{Table: "devices", ID: d.ID, Value: d}}
	if changed {
		revisions, e := k.docChangesForDevice(d.ID)
		if e != nil {
			return e
		}
		changes = append(changes, revisions...)
	}
	return k.Store.Batch(changes...)
}

func (k *Core) docChangesForDevice(id string) ([]storage.Change, error) {
	bindings, e := storage.List[Binding](k.Store, "project_services")
	if e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	changes := []storage.Change{}
	for _, b := range bindings {
		if b.Deleted || b.DeviceID != id || seen[b.ProjectID] {
			continue
		}
		seen[b.ProjectID] = true
		p, e := k.ProjectByID(b.ProjectID)
		if e != nil {
			return nil, e
		}
		p.DocRevision++
		p.UpdatedAt = time.Now().UTC()
		changes = append(changes, storage.Change{Table: "projects", ID: p.ID, Value: p})
	}
	return changes, nil
}
func (k *Core) ReportAvailability(ctx context.Context, id, state, msg string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	var d c.Device
	if e := k.Store.Get("devices", id, &d); e != nil {
		return e
	}
	d.Availability = state
	d.LastError = msg
	return k.Store.Put("devices", id, d)
}
func (k *Core) Recover() error {
	ds, e := storage.List[c.Device](k.Store, "devices")
	if e != nil {
		return e
	}
	for _, d := range ds {
		d.Availability = "unknown"
		d.LastError = "等待连接探测"
		if e = k.Store.Put("devices", d.ID, d); e != nil {
			return e
		}
	}
	rs, e := storage.List[Record](k.Store, "invocations")
	if e != nil {
		return e
	}
	for _, r := range rs {
		if r.Status == "running" {
			now := time.Now().UTC()
			r.Status = "interrupted"
			r.FinishedAt = &now
			r.Error = &c.Error{Code: "OUTCOME_UNKNOWN", Message: "进程中断，调用未重放", Outcome: "unknown"}
			if e = k.Store.Put("invocations", r.RequestID, r); e != nil {
				return e
			}
		}
	}
	return nil
}
func (k *Core) Devices() ([]c.Device, error) { return storage.List[c.Device](k.Store, "devices") }
func (k *Core) Device(id string) (c.Device, error) {
	var d c.Device
	if e := k.Store.Get("devices", id, &d); e != nil {
		return d, c.Fail("DEVICE_NOT_FOUND", "设备不存在")
	}
	return d, nil
}
func (k *Core) CreateDevice(ctx context.Context, template, name string, config json.RawMessage) (c.Device, error) {
	if strings.TrimSpace(name) == "" {
		return c.Device{}, c.Fail("INVALID_ARGUMENT", "请输入设备名称")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	d, e := k.Provisioner.Create(ctx, template, name, config)
	if e != nil {
		return d, e
	}
	e = k.Store.Put("devices", d.ID, d)
	if e != nil {
		_ = k.Provisioner.Remove(ctx, d.ID)
	}
	return d, e
}
func (k *Core) UpdateDevice(ctx context.Context, id, name string, config json.RawMessage, revision int) (c.Device, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	d, e := k.Device(id)
	if e != nil {
		return d, e
	}
	if revision != d.Revision {
		return d, c.Fail("CONFIG_CONFLICT", "设备配置已变化，请刷新")
	}
	if strings.TrimSpace(name) == "" {
		return d, c.Fail("INVALID_ARGUMENT", "请输入设备名称")
	}
	d, e = k.Provisioner.Update(ctx, id, name, config)
	if e != nil {
		return d, e
	}
	d.Revision = revision + 1
	changes, e := k.docChangesForDevice(id)
	if e != nil {
		return d, e
	}
	changes = append(changes, storage.Change{Table: "devices", ID: id, Value: d})
	e = k.Store.Batch(changes...)
	return d, e
}
func (k *Core) RefreshDeviceCapabilities(ctx context.Context, id string) (c.Device, error) {
	d, err := k.Device(id)
	if err != nil {
		return d, err
	}
	p, ok := k.Provisioner.(c.DeviceCapabilityRefresher)
	if !ok {
		return d, c.Fail("INVALID_ARGUMENT", "该接入不支持刷新能力")
	}
	config, err := p.PrepareCapabilityRefresh(ctx, id, d.Revision)
	if err != nil {
		return d, err
	}
	return k.UpdateDevice(ctx, id, d.DisplayName, config, d.Revision)
}

func (k *Core) DeleteDevice(ctx context.Context, id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, e := k.Device(id); e != nil {
		return e
	}
	bs, e := storage.List[Binding](k.Store, "project_services")
	if e != nil {
		return e
	}
	refs := []string{}
	for _, b := range bs {
		if !b.Deleted && b.DeviceID == id {
			var p Project
			_ = k.Store.Get("projects", b.ProjectID, &p)
			refs = append(refs, p.Key+"/"+b.Key)
		}
	}
	if len(refs) > 0 {
		return c.Fail("RESOURCE_REFERENCED", "设备仍被引用："+strings.Join(refs, ", "))
	}
	if e = k.Provisioner.Remove(ctx, id); e != nil {
		return e
	}
	return k.Store.Delete("devices", id)
}
func (k *Core) Projects(p auth.Principal) ([]Project, error) {
	all, e := storage.List[Project](k.Store, "projects")
	out := []Project{}
	for _, v := range all {
		if !v.Deleted && k.allowed(p, v, "") == nil {
			out = append(out, v)
		}
	}
	return out, e
}
func (k *Core) ProjectByID(id string) (Project, error) {
	var p Project
	if e := k.Store.Get("projects", id, &p); e != nil || p.Deleted {
		return p, c.Fail("PROJECT_NOT_FOUND", "项目不存在")
	}
	return p, nil
}
func (k *Core) Project(key string, p auth.Principal, scope string) (Project, error) {
	all, e := storage.List[Project](k.Store, "projects")
	if e != nil {
		return Project{}, e
	}
	for _, v := range all {
		if v.Key == key && !v.Deleted {
			if e = k.allowed(p, v, scope); e != nil {
				return Project{}, e
			}
			if !v.Enabled {
				return Project{}, c.Fail("PROJECT_DISABLED", "项目已禁用")
			}
			return v, nil
		}
	}
	return Project{}, c.Fail("PROJECT_NOT_FOUND", "项目不存在")
}
func (k *Core) allowed(p auth.Principal, v Project, scope string) error {
	if p.Kind == "user" {
		if p.Role == "admin" {
			return nil
		}
		for _, id := range v.MemberIDs {
			if id == p.ID {
				return nil
			}
		}
	} else if p.Kind == "token" && p.ProjectID == v.ID {
		if scope == "" {
			return nil
		}
		for _, s := range p.Scopes {
			if s == scope {
				return nil
			}
		}
	}
	return c.Fail("FORBIDDEN", "无权访问该项目或操作")
}
func (k *Core) SaveProject(p Project, create bool) (Project, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !keyPattern.MatchString(p.Key) || strings.TrimSpace(p.DisplayName) == "" {
		return p, c.Fail("INVALID_ARGUMENT", "项目key需为小写ASCII标识，且名称不能为空")
	}
	if create {
		all, e := storage.List[Project](k.Store, "projects")
		if e != nil {
			return p, e
		}
		for _, x := range all {
			if x.Key == p.Key {
				return p, c.Fail("CONFIG_CONFLICT", "项目key已使用，删除后也不能复用")
			}
		}
		p.ID = auth.ID()
		p.Revision = 1
		p.DocRevision = 1
		p.MemberIDs = []string{}
	} else {
		old, e := k.ProjectByID(p.ID)
		if e != nil {
			return p, e
		}
		if old.Key != p.Key {
			return p, c.Fail("INVALID_ARGUMENT", "项目key不可修改")
		}
		if old.Revision != p.Revision {
			return p, c.Fail("CONFIG_CONFLICT", "项目配置已变化，请刷新")
		}
		p.MemberIDs = old.MemberIDs
		p.Revision++
		p.DocRevision = old.DocRevision + 1
	}
	p.Deleted = false
	p.UpdatedAt = time.Now().UTC()
	return p, k.Store.Put("projects", p.ID, p)
}
func (k *Core) SetMembers(id string, ids []string, revision int) (Project, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	p, e := k.ProjectByID(id)
	if e != nil {
		return p, e
	}
	if revision != p.Revision {
		return p, c.Fail("CONFIG_CONFLICT", "项目配置已变化，请刷新")
	}
	for _, id := range ids {
		var u auth.User
		if k.Store.Get("users", id, &u) != nil {
			return p, c.Fail("INVALID_ARGUMENT", "成员用户不存在")
		}
	}
	p.MemberIDs = ids
	p.Revision++
	p.UpdatedAt = time.Now().UTC()
	return p, k.Store.Put("projects", p.ID, p)
}
func (k *Core) DeleteProject(id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	p, e := k.ProjectByID(id)
	if e != nil {
		return e
	}
	p.Deleted = true
	p.Enabled = false
	p.Revision++
	cs := []storage.Change{{Table: "projects", ID: id, Value: p}}
	bs, e := storage.List[Binding](k.Store, "project_services")
	if e != nil {
		return e
	}
	for _, b := range bs {
		if b.ProjectID == id {
			b.Deleted = true
			b.Enabled = false
			cs = append(cs, storage.Change{Table: "project_services", ID: b.ID, Value: b})
		}
	}
	ts, e := storage.List[auth.Token](k.Store, "access_tokens")
	if e != nil {
		return e
	}
	for _, t := range ts {
		if t.ProjectID == id {
			t.Enabled = false
			cs = append(cs, storage.Change{Table: "access_tokens", ID: t.ID, Value: t})
		}
	}
	p.MemberIDs = []string{}
	cs[0].Value = p
	return k.Store.Batch(cs...)
}
func compatible(a, b c.Service) bool {
	if a.Contract != b.Contract || a.ContractVersion != b.ContractVersion || a.Kind != b.Kind || a.SupportsVerification != b.SupportsVerification || a.ResultSource != b.ResultSource || len(a.Input.Properties) != len(b.Input.Properties) {
		return false
	}
	for key, v := range a.Input.Properties {
		x, ok := b.Input.Properties[key]
		if !ok || v.Type != x.Type || v.Unit != x.Unit {
			return false
		}
	}
	x, _ := json.Marshal(a.Output)
	y, _ := json.Marshal(b.Output)
	return string(x) == string(y)
}
func (k *Core) bindingState(b Binding) string {
	if b.DeviceID == "" {
		return "unbound"
	}
	d, e := k.Device(b.DeviceID)
	if e != nil {
		return "binding_invalid"
	}
	s, e := c.ServiceByKey(d, b.DeviceServiceKey)
	if e != nil || !compatible(b.Contract, s) {
		return "binding_invalid"
	}
	for key, p := range b.Contract.Input.Properties {
		n := s.Input.Properties[key]
		if n.Minimum != nil && (p.Minimum == nil || *p.Minimum < *n.Minimum) {
			return "binding_invalid"
		}
		if n.Maximum != nil && (p.Maximum == nil || *p.Maximum > *n.Maximum) {
			return "binding_invalid"
		}
		if n.MultipleOf != nil && p.MultipleOf != nil {
			q := *p.MultipleOf / *n.MultipleOf
			if q < 1 || math.Abs(q-math.Round(q)) > 1e-8*math.Max(1, math.Abs(q)) {
				return "binding_invalid"
			}
		}
	}
	return "valid"
}
func (k *Core) Bindings(project string, usable bool) ([]Binding, error) {
	all, e := storage.List[Binding](k.Store, "project_services")
	out := []Binding{}
	for _, b := range all {
		if b.ProjectID == project && !b.Deleted {
			b.BindingState = k.bindingState(b)
			if !usable || (b.Enabled && b.BindingState == "valid") {
				out = append(out, b)
			}
		}
	}
	return out, e
}
func validateDefaults(s c.Schema, m map[string]any) error {
	for key, v := range m {
		p, ok := s.Properties[key]
		if !ok {
			return c.Fail("INVALID_ARGUMENT", "默认值含未知参数")
		}
		if e := p.Validate(v); e != nil {
			return e
		}
	}
	return nil
}
func (k *Core) SaveBinding(b Binding, create bool) (Binding, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	p, e := k.ProjectByID(b.ProjectID)
	if e != nil {
		return b, e
	}
	if !keyPattern.MatchString(b.Key) || strings.TrimSpace(b.DisplayName) == "" {
		return b, c.Fail("INVALID_ARGUMENT", "服务key或名称无效")
	}
	var old Binding
	if create {
		all, e := storage.List[Binding](k.Store, "project_services")
		if e != nil {
			return b, e
		}
		for _, x := range all {
			if x.ProjectID == b.ProjectID && x.Key == b.Key {
				return b, c.Fail("CONFIG_CONFLICT", "服务key已使用，删除后也不能复用")
			}
		}
		b.ID = auth.ID()
		b.Revision = 1
	} else {
		if k.Store.Get("project_services", b.ID, &old) != nil || old.Deleted || old.ProjectID != b.ProjectID {
			return b, c.Fail("SERVICE_NOT_FOUND", "服务不存在")
		}
		if old.Revision != b.Revision {
			return b, c.Fail("CONFIG_CONFLICT", "服务配置已变化，请刷新")
		}
		if old.Key != b.Key {
			return b, c.Fail("INVALID_ARGUMENT", "服务key不可修改")
		}
		b.Revision++
	}
	if b.DeviceID != "" {
		d, e := k.Device(b.DeviceID)
		if e != nil {
			return b, e
		}
		s, e := c.ServiceByKey(d, b.DeviceServiceKey)
		if e != nil {
			return b, e
		}
		if !create && old.Contract.Contract != "" && !compatible(old.Contract, s) {
			return b, c.Fail("BINDING_INVALID", "新设备服务契约不兼容")
		}
		input, e := s.Input.Tighten(b.Constraints)
		if e != nil {
			return b, e
		}
		if !create && old.Contract.Contract != "" {
			for key, prev := range old.Contract.Input.Properties {
				n := input.Properties[key]
				if prev.Minimum != nil && (n.Minimum == nil || *n.Minimum < *prev.Minimum) {
					return b, c.Fail("INVALID_ARGUMENT", "不能放宽已有服务下限")
				}
				if prev.Maximum != nil && (n.Maximum == nil || *n.Maximum > *prev.Maximum) {
					return b, c.Fail("INVALID_ARGUMENT", "不能放宽已有服务上限")
				}
			}
		}
		s.Input = input
		b.Contract = s
	} else {
		b.DeviceServiceKey = ""
		b.Contract = old.Contract
	}
	if b.DefaultParameters == nil {
		b.DefaultParameters = map[string]any{}
	}
	if b.Contract.Contract != "" {
		if e = validateDefaults(b.Contract.Input, b.DefaultParameters); e != nil {
			return b, e
		}
	}
	b.Deleted = false
	b.BindingState = k.bindingState(b)
	p.DocRevision++
	p.UpdatedAt = time.Now().UTC()
	e = k.Store.Batch(storage.Change{Table: "project_services", ID: b.ID, Value: b}, storage.Change{Table: "projects", ID: p.ID, Value: p})
	return b, e
}
func (k *Core) DeleteBinding(project, id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	p, e := k.ProjectByID(project)
	if e != nil {
		return e
	}
	var b Binding
	if k.Store.Get("project_services", id, &b) != nil || b.ProjectID != project || b.Deleted {
		return c.Fail("SERVICE_NOT_FOUND", "服务不存在")
	}
	b.Deleted = true
	b.Enabled = false
	b.Revision++
	p.DocRevision++
	p.UpdatedAt = time.Now().UTC()
	return k.Store.Batch(storage.Change{Table: "project_services", ID: id, Value: b}, storage.Change{Table: "projects", ID: project, Value: p})
}
func (k *Core) Invoke(ctx context.Context, p auth.Principal, projectKey, serviceKey string, params map[string]any) (Record, error) {
	k.mu.Lock()
	project, e := k.Project(projectKey, p, "services:invoke")
	if e != nil {
		k.mu.Unlock()
		return Record{}, e
	}
	bs, e := k.Bindings(project.ID, false)
	if e != nil {
		k.mu.Unlock()
		return Record{}, e
	}
	var found *Binding
	for _, b := range bs {
		if b.Key == serviceKey {
			x := b
			found = &x
			break
		}
	}
	if found == nil {
		k.mu.Unlock()
		return Record{}, c.Fail("SERVICE_NOT_FOUND", "服务不存在")
	}
	b := *found
	if !b.Enabled {
		k.mu.Unlock()
		return Record{}, c.Fail("SERVICE_DISABLED", "服务已禁用")
	}
	if b.BindingState == "unbound" {
		k.mu.Unlock()
		return Record{}, c.Fail("SERVICE_UNBOUND", "服务未绑定设备")
	}
	if b.BindingState != "valid" {
		k.mu.Unlock()
		return Record{}, c.Fail("BINDING_INVALID", "服务绑定失效")
	}
	m, e := b.Contract.Input.Parameters(params, b.DefaultParameters)
	if e != nil {
		k.mu.Unlock()
		return Record{}, e
	}
	r := Record{RequestID: auth.ID(), SubjectID: p.ID, SubjectKind: p.Kind, ProjectID: project.ID, ProjectKey: project.Key, ServiceKey: b.Key, ServiceRevision: b.Revision, DeviceID: b.DeviceID, DeviceServiceKey: b.DeviceServiceKey, Parameters: m, Status: "running", StartedAt: time.Now().UTC()}
	r, e = k.begin(r)
	k.mu.Unlock()
	if e != nil {
		return r, e
	}
	return k.execute(ctx, r, b.Contract)
}
func (k *Core) InvokeDevice(ctx context.Context, p auth.Principal, id, key string, params map[string]any) (Record, error) {
	if p.Kind != "user" || p.Role != "admin" {
		return Record{}, c.Fail("FORBIDDEN", "仅管理员可直接调试设备")
	}
	d, e := k.Device(id)
	if e != nil {
		return Record{}, e
	}
	s, e := c.ServiceByKey(d, key)
	if e != nil {
		return Record{}, e
	}
	m, e := s.Input.Parameters(params, nil)
	if e != nil {
		return Record{}, e
	}
	r := Record{RequestID: auth.ID(), SubjectID: p.ID, SubjectKind: p.Kind, ServiceKey: key, ServiceRevision: d.Revision, DeviceID: id, DeviceServiceKey: key, Parameters: m, Status: "running", StartedAt: time.Now().UTC()}
	if r, e = k.begin(r); e != nil {
		return r, e
	}
	return k.execute(ctx, r, s)
}

func (k *Core) begin(r Record) (Record, error) {
	if err := k.Store.Put("invocations", r.RequestID, r); err != nil {
		now := time.Now().UTC()
		r.Status, r.FinishedAt = "failed", &now
		r.Error = &c.Error{Code: "INTERNAL_ERROR", Message: "调用开始记录无法保存，未发送设备请求", Outcome: "not_started"}
		return r, r.Error
	}
	return r, nil
}

func (k *Core) execute(ctx context.Context, r Record, s c.Service) (Record, error) {
	timeout := time.Duration(s.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, e := k.Invoker.Invoke(ctx, c.Invocation{RequestID: r.RequestID, DeviceID: r.DeviceID, ServiceKey: r.DeviceServiceKey, Parameters: r.Parameters, Deadline: time.Now().Add(timeout)})
	now := time.Now().UTC()
	r.FinishedAt = &now
	r.DurationMS = now.Sub(r.StartedAt).Milliseconds()
	if e != nil {
		r.Status = "failed"
		r.Error = c.AsError(e)
	} else if ve := s.Output.Validate(result.Output); ve != nil {
		e = &c.Error{Code: "PROTOCOL_ERROR", Message: "设备返回不符合输出契约", Outcome: "unknown"}
		r.Status = "failed"
		r.Error = c.AsError(e)
	} else {
		r.Status = "succeeded"
		r.Result = result.Output
		r.Confirmation = result.Confirmation
		r.Source = result.Source
		if !result.ObservedAt.IsZero() {
			r.ObservedAt = &result.ObservedAt
		}
	}
	if se := k.Store.Put("invocations", r.RequestID, r); se != nil {
		err := &c.Error{Code: "INTERNAL_ERROR", Message: "设备调用已结束，但结果持久化失败，请按请求ID核查", Outcome: "unknown"}
		r.Status, r.Error = "failed", err
		return r, err
	}
	return r, e
}
func (k *Core) Records(project, service, subject, status, device, errorCode string, page, limit int) (map[string]any, error) {
	rs, e := storage.List[Record](k.Store, "invocations")
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, r := range rs {
		if (project == "" || project == r.ProjectID) && (service == "" || service == r.ServiceKey) && (subject == "" || subject == r.SubjectID) && (status == "" || status == r.Status) && (device == "" || device == r.DeviceID) && (errorCode == "" || (r.Error != nil && r.Error.Code == errorCode)) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	total := len(out)
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 25
	}
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return map[string]any{"items": out[start:end], "total": total, "page": page, "page_size": limit}, nil
}
