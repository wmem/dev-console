package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"devconsole/internal/auth"
	"devconsole/internal/buildinfo"
	c "devconsole/internal/contracts"
	"devconsole/internal/core"
)

type Server struct {
	Core    *core.Core
	Auth    *auth.Auth
	BaseURL string
	Assets  fs.FS
	mux     *http.ServeMux
}

func New(k *core.Core, a *auth.Auth, base string, assets fs.FS) *Server {
	s := &Server{Core: k, Auth: a, BaseURL: strings.TrimRight(base, "/"), Assets: assets, mux: http.NewServeMux()}
	s.routes()
	return s
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'")
	defer func() {
		if e := recover(); e != nil {
			slog.Error("HTTP处理异常")
			s.fail(w, c.Fail("INTERNAL_ERROR", "内部处理失败"))
		}
	}()
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
	}
	s.mux.ServeHTTP(w, r)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) fail(w http.ResponseWriter, e error) {
	ce := c.AsError(e)
	status := 500
	switch ce.Code {
	case "INVALID_ARGUMENT":
		status = 400
	case "UNAUTHENTICATED":
		status = 401
	case "FORBIDDEN", "PROJECT_DISABLED", "SERVICE_DISABLED":
		status = 403
	case "PROJECT_NOT_FOUND", "SERVICE_NOT_FOUND", "DEVICE_NOT_FOUND", "USER_NOT_FOUND", "NOT_FOUND":
		status = 404
	case "RESOURCE_BUSY", "SERVICE_UNBOUND", "BINDING_INVALID", "RESOURCE_REFERENCED", "CONFIG_CONFLICT":
		status = 409
	case "DEVICE_OFFLINE":
		status = 503
	case "TIMEOUT", "OUTCOME_UNKNOWN":
		status = 504
	case "DEVICE_ERROR", "PROTOCOL_ERROR", "VERIFICATION_FAILED":
		status = 502
	case "RATE_LIMITED":
		status = 429
	}
	respond(w, status, map[string]any{"request_id": auth.ID(), "status": "failed", "error": ce})
}
func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1024*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return c.Fail("INVALID_ARGUMENT", "请求JSON格式、字段或大小不合法")
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return c.Fail("INVALID_ARGUMENT", "请求只能包含一个JSON对象")
	}
	return nil
}

type endpoint func(http.ResponseWriter, *http.Request, auth.Principal) error

func (s *Server) handle(pattern string, admin bool, f endpoint) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		p, e := s.principal(r)
		if e != nil {
			s.fail(w, e)
			return
		}
		if admin && (p.Kind != "user" || p.Role != "admin") {
			s.fail(w, c.Fail("FORBIDDEN", "仅管理员可管理平台"))
			return
		}
		if p.Kind == "user" && r.Method != "GET" && r.Method != "HEAD" && !auth.Equal(p.CSRF, r.Header.Get("X-CSRF-Token")) {
			s.fail(w, c.Fail("FORBIDDEN", "CSRF校验失败"))
			return
		}
		if e = f(w, r, p); e != nil {
			s.fail(w, e)
		}
	})
}
func (s *Server) principal(r *http.Request) (auth.Principal, error) {
	h := r.Header.Get("Authorization")
	if h != "" {
		if !strings.HasPrefix(h, "Bearer ") {
			return auth.Principal{}, c.Fail("UNAUTHENTICATED", "鉴权方式无效")
		}
		return s.Auth.Bearer(strings.TrimPrefix(h, "Bearer "))
	}
	cookie, e := r.Cookie("dc_session")
	if e != nil {
		return auth.Principal{}, c.Fail("UNAUTHENTICATED", "请先登录")
	}
	return s.Auth.Cookie(cookie.Value)
}
func (s *Server) cookie(w http.ResponseWriter, raw string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: "dc_session", Value: raw, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: strings.HasPrefix(s.BaseURL, "https://"), MaxAge: maxAge})
}
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"status": "ok", "version": buildinfo.Version, "commit": buildinfo.Commit})
	})
	s.mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if e := decode(r, &in); e != nil {
			s.fail(w, e)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		raw, u, e := s.Auth.Login(in.Username, in.Password, host)
		if e != nil {
			s.fail(w, e)
			return
		}
		s.cookie(w, raw, 43200)
		p, e := s.Auth.Cookie(raw)
		if e != nil {
			s.fail(w, e)
			return
		}
		respond(w, 200, map[string]any{"user": u, "csrf": p.CSRF})
	})
	s.handle("GET /api/v1/auth/me", false, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if p.Kind != "user" {
			return c.Fail("FORBIDDEN", "需要Web登录")
		}
		us, e := s.Auth.Users()
		if e != nil {
			return e
		}
		for _, u := range us {
			if u.ID == p.ID {
				respond(w, 200, map[string]any{"user": u, "csrf": p.CSRF, "public_base_url": s.BaseURL})
				return nil
			}
		}
		return c.Fail("UNAUTHENTICATED", "用户不存在")
	})
	s.handle("POST /api/v1/auth/logout", false, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if p.Kind != "user" {
			return c.Fail("FORBIDDEN", "需要Web登录")
		}
		cookie, _ := r.Cookie("dc_session")
		if e := s.Auth.Logout(cookie.Value); e != nil {
			return e
		}
		s.cookie(w, "", -1)
		respond(w, 200, map[string]any{"ok": true})
		return nil
	})
	s.handle("PUT /api/v1/auth/password", false, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if p.Kind != "user" {
			return c.Fail("FORBIDDEN", "需要Web登录")
		}
		var in struct {
			Old      string `json:"old_password"`
			Password string `json:"password"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		if e := s.Auth.Password(p.ID, in.Old, in.Password, false); e != nil {
			return e
		}
		s.cookie(w, "", -1)
		respond(w, 200, map[string]any{"ok": true})
		return nil
	})
	s.handle("GET /api/v1/admin/users", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		v, e := s.Auth.Users()
		if e == nil {
			respond(w, 200, v)
		}
		return e
	})
	s.handle("POST /api/v1/admin/users", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		v, e := s.Auth.Create(in.Username, in.Password, in.Role)
		if e == nil {
			respond(w, 201, v)
		}
		return e
	})
	s.handle("PATCH /api/v1/admin/users/{id}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			Role    string `json:"role"`
			Enabled bool   `json:"enabled"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		v, e := s.Auth.Update(r.PathValue("id"), in.Role, in.Enabled)
		if e == nil {
			respond(w, 200, v)
		}
		return e
	})
	s.handle("POST /api/v1/admin/users/{id}/reset-password", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			Password string `json:"password"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		if e := s.Auth.Password(r.PathValue("id"), "", in.Password, true); e != nil {
			return e
		}
		respond(w, 200, map[string]any{"ok": true})
		return nil
	})
	s.handle("GET /api/v1/admin/access-templates", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		respond(w, 200, s.Core.Provisioner.ListTemplates(r.Context()))
		return nil
	})
	s.handle("GET /api/v1/admin/access-templates/{key}/connections", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		choices, e := s.Core.Provisioner.DiscoverConnections(r.Context(), r.PathValue("key"))
		if e == nil {
			respond(w, 200, choices)
		}
		return e
	})
	s.handle("GET /api/v1/admin/devices", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		v, e := s.Core.Devices()
		if e == nil {
			respond(w, 200, v)
		}
		return e
	})
	s.handle("POST /api/v1/admin/devices", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			Template string          `json:"template_key"`
			Name     string          `json:"display_name"`
			Config   json.RawMessage `json:"connection_config"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		v, e := s.Core.CreateDevice(r.Context(), in.Template, in.Name, in.Config)
		if e != nil {
			return e
		}
		_ = s.Core.Provisioner.Test(r.Context(), v.ID)
		v, e = s.Core.Device(v.ID)
		if e == nil {
			respond(w, 201, v)
		}
		return e
	})
	s.handle("GET /api/v1/admin/devices/{id}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		v, e := s.Core.Device(r.PathValue("id"))
		if e != nil {
			return e
		}
		cfg, e := s.Core.Provisioner.Config(r.Context(), v.ID)
		if e == nil {
			respond(w, 200, map[string]any{"device": v, "connection_config": cfg})
		}
		return e
	})
	s.handle("PATCH /api/v1/admin/devices/{id}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			Name     string          `json:"display_name"`
			Config   json.RawMessage `json:"connection_config"`
			Revision int             `json:"revision"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		v, e := s.Core.UpdateDevice(r.Context(), r.PathValue("id"), in.Name, in.Config, in.Revision)
		if e == nil {
			respond(w, 200, v)
		}
		return e
	})
	s.handle("DELETE /api/v1/admin/devices/{id}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if e := s.Core.DeleteDevice(r.Context(), r.PathValue("id")); e != nil {
			return e
		}
		respond(w, 200, map[string]any{"ok": true})
		return nil
	})
	s.handle("POST /api/v1/admin/devices/{id}/connection-test", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if e := s.Core.Provisioner.Test(r.Context(), r.PathValue("id")); e != nil {
			return e
		}
		respond(w, 200, map[string]any{"ok": true})
		return nil
	})
	s.handle("GET /api/v1/admin/devices/{id}/services", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		v, e := s.Core.Device(r.PathValue("id"))
		if e == nil {
			respond(w, 200, v.Services)
		}
		return e
	})
	s.handle("POST /api/v1/admin/devices/{id}/services/{key}/invoke", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			Parameters map[string]any `json:"parameters"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		if in.Parameters == nil {
			return c.Fail("INVALID_ARGUMENT", "parameters必须是JSON对象")
		}
		v, e := s.Core.InvokeDevice(r.Context(), p, r.PathValue("id"), r.PathValue("key"), in.Parameters)
		return s.invocation(w, v, e)
	})
	s.projectRoutes()
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, c.Fail("NOT_FOUND", "API不存在或请求方法不支持"))
	})
	s.mux.HandleFunc("/", s.static)
}
func (s *Server) invocation(w http.ResponseWriter, r core.Record, e error) error {
	if e != nil {
		if r.RequestID == "" {
			return e
		}
		ce := c.AsError(e)
		status := 502
		if ce.Code == "RESOURCE_BUSY" || ce.Code == "BINDING_INVALID" {
			status = 409
		} else if ce.Code == "DEVICE_OFFLINE" {
			status = 503
		} else if ce.Code == "TIMEOUT" || ce.Code == "OUTCOME_UNKNOWN" {
			status = 504
		} else if ce.Code == "INTERNAL_ERROR" {
			status = 500
		}
		respond(w, status, r)
		return nil
	}
	respond(w, 200, r)
	return nil
}
func (s *Server) projectRoutes() {
	s.handle("GET /api/v1/admin/projects", true, s.listProjects)
	s.handle("GET /api/v1/projects", false, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if p.Kind != "user" {
			return c.Fail("FORBIDDEN", "Token不能枚举项目")
		}
		return s.listProjects(w, r, p)
	})
	s.handle("POST /api/v1/admin/projects", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in core.Project
		if e := decode(r, &in); e != nil {
			return e
		}
		v, e := s.Core.SaveProject(in, true)
		if e == nil {
			respond(w, 201, v)
		}
		return e
	})
	s.handle("PATCH /api/v1/admin/projects/{id}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in core.Project
		if e := decode(r, &in); e != nil {
			return e
		}
		in.ID = r.PathValue("id")
		v, e := s.Core.SaveProject(in, false)
		if e == nil {
			respond(w, 200, v)
		}
		return e
	})
	s.handle("DELETE /api/v1/admin/projects/{id}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if e := s.Core.DeleteProject(r.PathValue("id")); e != nil {
			return e
		}
		respond(w, 200, map[string]any{"ok": true})
		return nil
	})
	s.handle("GET /api/v1/admin/projects/{id}/members", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		v, e := s.Core.ProjectByID(r.PathValue("id"))
		if e == nil {
			respond(w, 200, map[string]any{"member_ids": v.MemberIDs, "revision": v.Revision})
		}
		return e
	})
	s.handle("PUT /api/v1/admin/projects/{id}/members", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			IDs      []string `json:"member_ids"`
			Revision int      `json:"revision"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		v, e := s.Core.SetMembers(r.PathValue("id"), in.IDs, in.Revision)
		if e == nil {
			respond(w, 200, v)
		}
		return e
	})
	s.handle("GET /api/v1/admin/projects/{id}/services", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if _, e := s.Core.ProjectByID(r.PathValue("id")); e != nil {
			return e
		}
		v, e := s.Core.Bindings(r.PathValue("id"), false)
		if e == nil {
			respond(w, 200, v)
		}
		return e
	})
	for _, method := range []string{"POST", "PATCH"} {
		create := method == "POST"
		pattern := method + " /api/v1/admin/projects/{id}/services"
		if !create {
			pattern += "/{serviceId}"
		}
		s.handle(pattern, true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
			var in core.Binding
			if e := decode(r, &in); e != nil {
				return e
			}
			in.ProjectID = r.PathValue("id")
			if !create {
				in.ID = r.PathValue("serviceId")
			}
			v, e := s.Core.SaveBinding(in, create)
			if e == nil {
				status := 200
				if create {
					status = 201
				}
				respond(w, status, v)
			}
			return e
		})
	}
	s.handle("DELETE /api/v1/admin/projects/{id}/services/{serviceId}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if e := s.Core.DeleteBinding(r.PathValue("id"), r.PathValue("serviceId")); e != nil {
			return e
		}
		respond(w, 200, map[string]any{"ok": true})
		return nil
	})
	s.handle("GET /api/v1/admin/projects/{id}/tokens", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if _, e := s.Core.ProjectByID(r.PathValue("id")); e != nil {
			return e
		}
		v, e := s.Auth.Tokens(r.PathValue("id"))
		if e == nil {
			respond(w, 200, v)
		}
		return e
	})
	s.handle("POST /api/v1/admin/projects/{id}/tokens", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if _, e := s.Core.ProjectByID(r.PathValue("id")); e != nil {
			return e
		}
		var in struct {
			Name      string     `json:"name"`
			Scopes    []string   `json:"scopes"`
			ExpiresAt *time.Time `json:"expires_at"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		v, raw, e := s.Auth.NewToken(r.PathValue("id"), in.Name, in.Scopes, in.ExpiresAt)
		if e == nil {
			respond(w, 201, map[string]any{"token": v, "secret": raw})
		}
		return e
	})
	s.handle("DELETE /api/v1/admin/projects/{id}/tokens/{tokenId}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if e := s.Auth.Revoke(r.PathValue("id"), r.PathValue("tokenId")); e != nil {
			return e
		}
		respond(w, 200, map[string]any{"ok": true})
		return nil
	})
	s.handle("GET /api/v1/admin/invocations", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		return s.records(w, r, r.URL.Query().Get("project_id"))
	})
	s.handle("GET /api/v1/projects/{key}", false, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		v, e := s.Core.Project(r.PathValue("key"), p, "")
		if e == nil {
			if p.Kind == "token" {
				v.MemberIDs = nil
			}
			respond(w, 200, v)
		}
		return e
	})
	s.handle("GET /api/v1/projects/{key}/services", false, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		v, e := s.Core.Project(r.PathValue("key"), p, "services:read")
		if e != nil {
			return e
		}
		bs, e := s.Core.Bindings(v.ID, true)
		if e == nil {
			respond(w, 200, bs)
		}
		return e
	})
	s.handle("POST /api/v1/projects/{key}/services/{serviceKey}/invoke", false, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			Parameters map[string]any `json:"parameters"`
		}
		if e := decode(r, &in); e != nil {
			return e
		}
		if in.Parameters == nil {
			return c.Fail("INVALID_ARGUMENT", "parameters必须是JSON对象")
		}
		v, e := s.Core.Invoke(r.Context(), p, r.PathValue("key"), r.PathValue("serviceKey"), in.Parameters)
		return s.invocation(w, v, e)
	})
	for _, kind := range []string{"docs", "openapi.json"} {
		s.handle("GET /api/v1/projects/{key}/"+kind, false, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
			text, spec, e := s.Core.Document(r.PathValue("key"), p, s.BaseURL)
			if e != nil {
				return e
			}
			if kind == "docs" {
				w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
				if r.URL.Query().Get("download") == "1" {
					w.Header().Set("Content-Disposition", "attachment; filename=\""+r.PathValue("key")+".md\"")
				}
				_, e = w.Write([]byte(text))
				return e
			}
			respond(w, 200, spec)
			return nil
		})
	}
	s.handle("GET /api/v1/projects/{key}/invocations", false, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if p.Kind != "user" {
			return c.Fail("FORBIDDEN", "Token不能读取调用记录")
		}
		v, e := s.Core.Project(r.PathValue("key"), p, "")
		if e != nil {
			return e
		}
		return s.records(w, r, v.ID)
	})
}
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	v, e := s.Core.Projects(p)
	if e == nil {
		respond(w, 200, v)
	}
	return e
}
func (s *Server) records(w http.ResponseWriter, r *http.Request, project string) error {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("page_size"))
	v, e := s.Core.Records(project, q.Get("service"), q.Get("subject"), q.Get("status"), q.Get("device_id"), q.Get("error_code"), page, limit)
	if e == nil {
		respond(w, 200, v)
	}
	return e
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		s.fail(w, c.Fail("NOT_FOUND", "路径不存在"))
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	b, e := fs.ReadFile(s.Assets, name)
	if e != nil {
		if !errors.Is(e, fs.ErrNotExist) || strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
		b, e = fs.ReadFile(s.Assets, name)
		if e != nil {
			http.Error(w, "前端资源不可用", 500)
			return
		}
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	if r.Method != "HEAD" {
		_, _ = w.Write(b)
	}
}
