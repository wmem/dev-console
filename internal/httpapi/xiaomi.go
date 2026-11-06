package httpapi

import (
	"net/http"

	"devconsole/internal/access/xiaomi"
	"devconsole/internal/auth"
	c "devconsole/internal/contracts"
)

func (s *Server) xiaomiRoutes() {
	if s.xiaomi == nil {
		return
	}
	s.handle("GET /api/v1/admin/xiaomi/accounts", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		respond(w, 200, s.xiaomi.Accounts())
		return nil
	})
	s.handle("DELETE /api/v1/admin/xiaomi/accounts/{id}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if err := s.xiaomi.DeleteAccount(r.PathValue("id")); err != nil {
			return err
		}
		respond(w, 200, map[string]bool{"ok": true})
		return nil
	})
	s.handle("POST /api/v1/admin/xiaomi/accounts/{id}/refresh", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if err := s.xiaomi.Refresh(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
		respond(w, 200, map[string]bool{"ok": true})
		return nil
	})
	s.handle("POST /api/v1/admin/xiaomi/logins", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			Name      string `json:"display_name"`
			Region    string `json:"region"`
			AccountID string `json:"account_id"`
		}
		if err := decode(r, &in); err != nil {
			return err
		}
		v, err := s.xiaomi.StartLogin(r.Context(), p.ID, in.Name, in.Region, in.AccountID)
		if err == nil {
			respond(w, 201, v)
		}
		return err
	})
	s.handle("GET /api/v1/admin/xiaomi/logins/{id}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		v, err := s.xiaomi.Login(p.ID, r.PathValue("id"))
		if err == nil {
			respond(w, 200, v)
		}
		return err
	})
	s.handle("GET /api/v1/admin/xiaomi/logins/{id}/qr", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		data, contentType, err := s.xiaomi.QRImage(p.ID, r.PathValue("id"))
		if err == nil {
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(200)
			_, _ = w.Write(data)
		}
		return err
	})
	s.handle("DELETE /api/v1/admin/xiaomi/logins/{id}", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		if err := s.xiaomi.CancelLogin(p.ID, r.PathValue("id")); err != nil {
			return err
		}
		respond(w, 200, map[string]bool{"ok": true})
		return nil
	})
	s.handle("POST /api/v1/admin/xiaomi/accounts/{id}/discovery", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		v, err := s.xiaomi.Discover(r.Context(), r.PathValue("id"))
		if err == nil {
			respond(w, 200, v)
		}
		return err
	})
	s.handle("POST /api/v1/admin/xiaomi/accounts/{id}/preview", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			DiscoveryID string `json:"discovery_id"`
			DID         string `json:"did"`
		}
		if err := decode(r, &in); err != nil {
			return err
		}
		v, err := s.xiaomi.Preview(r.Context(), r.PathValue("id"), in.DiscoveryID, in.DID)
		if err == nil {
			respond(w, 200, v)
		}
		return err
	})
	s.handle("POST /api/v1/admin/xiaomi/accounts/{id}/imports", true, func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		var in struct {
			DiscoveryID string   `json:"discovery_id"`
			DIDs        []string `json:"dids"`
		}
		if err := decode(r, &in); err != nil {
			return err
		}
		if len(in.DIDs) == 0 || len(in.DIDs) > 20 {
			return c.Fail("INVALID_ARGUMENT", "每次请选择1至20台已预览的开关设备")
		}
		type item struct {
			DID    string    `json:"did"`
			Device *c.Device `json:"device,omitempty"`
			Error  *c.Error  `json:"error,omitempty"`
		}
		items := make([]item, 0, len(in.DIDs))
		for _, did := range in.DIDs {
			name, config, err := s.xiaomi.PrepareImport(r.PathValue("id"), in.DiscoveryID, did)
			if err != nil {
				items = append(items, item{DID: did, Error: c.AsError(err)})
				continue
			}
			d, err := s.Core.CreateDevice(r.Context(), xiaomi.TemplateKey, name, config)
			if err != nil {
				items = append(items, item{DID: did, Error: c.AsError(err)})
			} else {
				items = append(items, item{DID: did, Device: &d})
			}
		}
		respond(w, 200, map[string]any{"items": items})
		return nil
	})
}
