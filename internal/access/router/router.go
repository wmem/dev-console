// Package router 在静态组装的设备接入模块间分发配置和调用，核心不解释厂商协议。
package router

import (
	"context"
	"encoding/json"

	c "devconsole/internal/contracts"
	"devconsole/internal/storage"
)

type Backend struct {
	Route       string
	Provisioner c.DeviceProvisioner
	Invoker     c.DeviceInvoker
}

type Router struct {
	store    *storage.Store
	backends []Backend
}

func New(store *storage.Store, backends ...Backend) *Router {
	return &Router{store: store, backends: backends}
}

func (r *Router) template(ctx context.Context, key string) (Backend, error) {
	for _, b := range r.backends {
		for _, t := range b.Provisioner.ListTemplates(ctx) {
			if t.Key == key {
				return b, nil
			}
		}
	}
	return Backend{}, c.Fail("INVALID_ARGUMENT", "未知接入模板")
}

func (r *Router) device(id string) (Backend, error) {
	var d c.Device
	if err := r.store.Get("devices", id, &d); err != nil {
		return Backend{}, c.Fail("DEVICE_NOT_FOUND", "设备不存在")
	}
	for _, b := range r.backends {
		if b.Route == d.AccessRoute {
			return b, nil
		}
	}
	return Backend{}, c.Fail("DEVICE_OFFLINE", "设备接入路由不可用")
}

func (r *Router) ListTemplates(ctx context.Context) []c.Template {
	out := []c.Template{}
	for _, b := range r.backends {
		out = append(out, b.Provisioner.ListTemplates(ctx)...)
	}
	return out
}

func (r *Router) DiscoverConnections(ctx context.Context, key string) ([]c.ConnectionChoice, error) {
	b, err := r.template(ctx, key)
	if err != nil {
		return nil, err
	}
	return b.Provisioner.DiscoverConnections(ctx, key)
}

func (r *Router) Create(ctx context.Context, key, name string, config json.RawMessage) (c.Device, error) {
	b, err := r.template(ctx, key)
	if err != nil {
		return c.Device{}, err
	}
	return b.Provisioner.Create(ctx, key, name, config)
}

func (r *Router) Update(ctx context.Context, id, name string, config json.RawMessage) (c.Device, error) {
	b, err := r.device(id)
	if err != nil {
		return c.Device{}, err
	}
	return b.Provisioner.Update(ctx, id, name, config)
}

func (r *Router) PrepareCapabilityRefresh(ctx context.Context, id string, revision int) (json.RawMessage, error) {
	b, err := r.device(id)
	if err != nil {
		return nil, err
	}
	if p, ok := b.Provisioner.(c.DeviceCapabilityRefresher); ok {
		return p.PrepareCapabilityRefresh(ctx, id, revision)
	}
	return nil, c.Fail("INVALID_ARGUMENT", "该设备不支持刷新能力")
}

func (r *Router) Remove(ctx context.Context, id string) error {
	b, err := r.device(id)
	if err != nil {
		return err
	}
	return b.Provisioner.Remove(ctx, id)
}

func (r *Router) Test(ctx context.Context, id string) error {
	b, err := r.device(id)
	if err != nil {
		return err
	}
	return b.Provisioner.Test(ctx, id)
}

func (r *Router) Config(ctx context.Context, id string) (json.RawMessage, error) {
	b, err := r.device(id)
	if err != nil {
		return nil, err
	}
	return b.Provisioner.Config(ctx, id)
}

func (r *Router) Invoke(ctx context.Context, request c.Invocation) (c.Result, error) {
	b, err := r.device(request.DeviceID)
	if err != nil {
		return c.Result{}, err
	}
	return b.Invoker.Invoke(ctx, request)
}
