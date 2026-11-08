package httpapi

import (
	"encoding/json"

	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/domain"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/model"
	"github.com/Axolotl-Launcher/Axolotl-Update-Server-Golang/internal/service"
	"github.com/gin-gonic/gin"
)

func (v *Server) webhook(c *gin.Context) {
	raw, e := readBody(c)
	if e != nil {
		v.fail(c, e)
		return
	}
	if e = v.Service.AuthenticateWebhook(raw, c.GetHeader("X-Webhook-Timestamp"), c.GetHeader("X-Webhook-Signature")); e != nil {
		v.fail(c, e)
		return
	}
	out, e := v.Service.Publish(c.Request.Context(), raw)
	if e != nil {
		v.fail(c, e)
		return
	}
	v.respond(c, 200, out)
}

func (v *Server) adminBody(c *gin.Context, force bool) (service.AdminMeta, *bool, bool) {
	meta := service.AdminMeta{Operator: "admin", RequestID: c.GetString("request_id"), IP: c.GetString("client_ip")}
	if !v.admin(c) {
		return meta, nil, false
	}
	if _, e := domain.ParseVersion(c.Param("version")); e != nil {
		v.fail(c, e)
		return meta, nil, false
	}
	raw, e := readBody(c)
	if e != nil {
		v.fail(c, e)
		return meta, nil, false
	}
	body := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if e = json.Unmarshal(raw, &body); e != nil || body == nil {
			v.fail(c, domain.Err("invalid_admin_metadata", "The request body must be a JSON object."))
			return meta, nil, false
		}
	}
	for key, dst := range map[string]*string{"operator": &meta.Operator, "reason": &meta.Reason} {
		if b, ok := body[key]; ok {
			if string(b) == "null" || json.Unmarshal(b, dst) != nil {
				v.fail(c, domain.Err("invalid_admin_metadata", "operator and reason must be strings."))
				return meta, nil, false
			}
		}
	}
	var enabled *bool
	if force {
		b, ok := body["force_update"]
		if !ok || string(b) == "null" || json.Unmarshal(b, &enabled) != nil || enabled == nil {
			v.fail(c, domain.Err("invalid_force_update", "force_update must be a JSON boolean."))
			return meta, nil, false
		}
	}
	return meta, enabled, true
}
func (v *Server) revoke(c *gin.Context)  { v.changeState(c, false) }
func (v *Server) restore(c *gin.Context) { v.changeState(c, true) }
func (v *Server) changeState(c *gin.Context, restore bool) {
	meta, _, ok := v.adminBody(c, false)
	if !ok {
		return
	}
	out, e := v.Service.ChangeState(c.Request.Context(), c.Param("version"), restore, meta)
	if e != nil {
		v.fail(c, e)
		return
	}
	c.Header("Cache-Control", "no-store")
	v.respond(c, 200, out)
}
func (v *Server) forceUpdate(c *gin.Context) {
	meta, enabled, ok := v.adminBody(c, true)
	if !ok {
		return
	}
	out, e := v.Service.ForceUpdate(c.Request.Context(), c.Param("version"), *enabled, meta)
	if e != nil {
		v.fail(c, e)
		return
	}
	c.Header("Cache-Control", "no-store")
	v.respond(c, 200, out)
}
func (v *Server) deleteVersion(c *gin.Context) {
	meta, _, ok := v.adminBody(c, false)
	if !ok {
		return
	}
	out, e := v.Service.Delete(c.Request.Context(), c.Param("version"), meta)
	if e != nil {
		v.fail(c, e)
		return
	}
	c.Header("Cache-Control", "no-store")
	v.respond(c, 200, out)
}
func (v *Server) audit(c *gin.Context) {
	if !v.admin(c) {
		return
	}
	logs := []model.AuditLog{}
	if e := v.Service.Store.DB.WithContext(c.Request.Context()).Order("created_at DESC").Find(&logs).Error; e != nil {
		v.fail(c, e)
		return
	}
	v.respond(c, 200, gin.H{"logs": logs})
}
