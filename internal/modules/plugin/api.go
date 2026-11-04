package plugin

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/wangwanjie/gately/internal/auth"
	"github.com/wangwanjie/gately/internal/httpx"
	"github.com/wangwanjie/gately/internal/logbuf"
	"github.com/wangwanjie/gately/internal/module"
)

// Routes 注册插件中心的管理接口与页面反代。
// 权限由框架统一处理：登录会话按角色放行（只读账号仅 GET），
// API 令牌按路径推导 plugin:read / plugin:write。
func (m *Module) Routes(r module.Router) {
	r.HandleFunc("GET /plugins", m.handleList)
	r.HandleFunc("POST /plugins", m.handleCreate)
	r.HandleFunc("GET /plugins/{id}", m.handleGet)
	r.HandleFunc("PUT /plugins/{id}", m.handleUpdate)
	r.HandleFunc("DELETE /plugins/{id}", m.handleDelete)
	r.HandleFunc("POST /plugins/{id}/start", m.handleStart)
	r.HandleFunc("POST /plugins/{id}/stop", m.handleStop)
	r.HandleFunc("POST /plugins/{id}/restart", m.handleRestart)
	r.HandleFunc("GET /plugins/{id}/status", m.handleStatus)
	// 页面反代：/pages/<id> 精确路径重定向到带斜杠的版本，
	// 使插件内相对链接与静态资源能正确解析。
	r.HandleFunc("GET /pages/{id}", m.handlePageRedirect)
	r.HandleFunc("/pages/{id}/", m.handlePageProxy)
	module.DocOf(r, "GET /plugins", module.Doc{Summary: "列出全部插件与运行状态", Resp: []View{}})
	module.DocOf(r, "POST /plugins", module.Doc{Summary: "注册插件（密钥仅本次返回）", Req: Plugin{}, Resp: map[string]any{}})
	module.DocOf(r, "GET /plugins/{id}", module.Doc{Summary: "查看单个插件", Resp: View{}})
	module.DocOf(r, "PUT /plugins/{id}", module.Doc{Summary: "修改插件（运行中且关键字段变化时重启）", Req: Plugin{}, Resp: View{}})
	module.DocOf(r, "DELETE /plugins/{id}", module.Doc{Summary: "停止并删除插件", Resp: map[string]bool{}})
	module.DocOf(r, "POST /plugins/{id}/start", module.Doc{Summary: "启动插件", Resp: View{}})
	module.DocOf(r, "POST /plugins/{id}/stop", module.Doc{Summary: "停止插件", Resp: View{}})
	module.DocOf(r, "POST /plugins/{id}/restart", module.Doc{Summary: "重启插件", Resp: View{}})
	module.DocOf(r, "GET /plugins/{id}/status", module.Doc{Summary: "查看插件运行状态", Resp: Status{}})
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]View, 0, len(m.cfg.Plugins))
	for _, pl := range m.cfg.Plugins {
		out = append(out, m.viewLocked(pl))
	}
	httpx.OK(w, out)
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in Plugin
	if err := httpx.ReadJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := in.Validate(); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	secret, err := newSecret()
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	in.Secret = secret
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, _, found := m.find(in.ID); found {
		httpx.Error(w, http.StatusBadRequest, "插件 ID %q 已存在", in.ID)
		return
	}
	np := in
	m.cfg.Plugins = append(m.cfg.Plugins, &np)
	rt := &runtime{plug: &np, state: StateStopped}
	m.rts[in.ID] = rt
	if err := m.saveLocked(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if in.Enabled {
		m.startLocked(rt)
	}
	v := m.viewLocked(&np)
	httpx.OK(w, map[string]any{"plugin": v, "secret": secret})
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, _, found := m.find(r.PathValue("id"))
	if !found {
		httpx.Error(w, http.StatusNotFound, "插件不存在")
		return
	}
	httpx.OK(w, m.viewLocked(p))
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var in Plugin
	if err := httpx.ReadJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, rt, found := m.find(r.PathValue("id"))
	if !found {
		httpx.Error(w, http.StatusNotFound, "插件不存在")
		return
	}
	in.ID = p.ID
	in.Secret = p.Secret
	if err := in.Validate(); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	restart := rt.state == StateRunning && (in.Kind != p.Kind || in.Command != p.Command ||
		strings.Join(in.Args, "\x00") != strings.Join(p.Args, "\x00") ||
		in.URL != p.URL || in.Port != p.Port || in.WorkDir != p.WorkDir)
	*p = in
	rt.plug = p
	if err := m.saveLocked(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	switch {
	case !in.Enabled:
		m.stopLocked(rt)
	case restart:
		m.stopLocked(rt)
		rt.manualStop = false
		rt.restarts = 0
		m.startLocked(rt)
	case rt.state == StateStopped:
		m.startLocked(rt)
	}
	httpx.OK(w, m.viewLocked(p))
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, rt, found := m.find(r.PathValue("id"))
	if !found {
		httpx.Error(w, http.StatusNotFound, "插件不存在")
		return
	}
	m.stopLocked(rt)
	delete(m.rts, r.PathValue("id"))
	for i, pl := range m.cfg.Plugins {
		if pl.ID == r.PathValue("id") {
			m.cfg.Plugins = append(m.cfg.Plugins[:i], m.cfg.Plugins[i+1:]...)
			break
		}
	}
	if err := m.saveLocked(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	httpx.OK(w, map[string]bool{"ok": true})
}

func (m *Module) handleStart(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, rt, found := m.find(r.PathValue("id"))
	if !found {
		httpx.Error(w, http.StatusNotFound, "插件不存在")
		return
	}
	if !p.Enabled {
		httpx.Error(w, http.StatusBadRequest, "插件未启用，请先启用")
		return
	}
	if rt.state == StateRunning {
		httpx.OK(w, m.viewLocked(p))
		return
	}
	rt.restarts = 0
	m.startLocked(rt)
	httpx.OK(w, m.viewLocked(p))
}

func (m *Module) handleStop(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, rt, found := m.find(r.PathValue("id"))
	if !found {
		httpx.Error(w, http.StatusNotFound, "插件不存在")
		return
	}
	m.stopLocked(rt)
	httpx.OK(w, m.viewLocked(p))
}

func (m *Module) handleRestart(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, rt, found := m.find(r.PathValue("id"))
	if !found {
		httpx.Error(w, http.StatusNotFound, "插件不存在")
		return
	}
	if !p.Enabled {
		httpx.Error(w, http.StatusBadRequest, "插件未启用，请先启用")
		return
	}
	m.stopLocked(rt)
	rt.manualStop = false
	rt.restarts = 0
	m.startLocked(rt)
	httpx.OK(w, m.viewLocked(p))
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, _, found := m.find(r.PathValue("id"))
	if !found {
		httpx.Error(w, http.StatusNotFound, "插件不存在")
		return
	}
	httpx.OK(w, m.viewLocked(p).Status)
}

func (m *Module) handlePageRedirect(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Path + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// handlePageProxy 把 /api/plugin/pages/<id>/... 反代给插件服务。
// 剥离登录会话凭据（Cookie、Authorization），改以 X-Gately-* 头表明身份，
// 插件不得直接信任浏览器带来的任何凭据，只能信任经 Gately 反代的请求。
func (m *Module) handlePageProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m.mu.Lock()
	_, rt, found := m.find(id)
	var target string
	var secret string
	var targetURL *url.URL
	if found && rt != nil && rt.plug.Enabled && rt.state == StateRunning && rt.target != nil {
		target = rt.target.String()
		targetURL = rt.target
		secret = rt.plug.Secret
	}
	m.mu.Unlock()
	if target == "" {
		if !found {
			httpx.Error(w, http.StatusNotFound, "插件不存在")
		} else {
			httpx.Error(w, http.StatusServiceUnavailable, "插件未运行")
		}
		return
	}
	prefix := "/api/plugin/pages/" + id
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == "" {
		rest = "/"
	}
	user := auth.UserFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = targetURL.Scheme
			pr.Out.URL.Host = targetURL.Host
			pr.Out.URL.Path = singleJoiningSlash(targetURL.Path, rest)
			pr.Out.Host = targetURL.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Set("X-Gately-Plugin", id)
			pr.Out.Header.Set("X-Gately-User", user)
			pr.Out.Header.Set("X-Gately-Role", role)
			pr.Out.Header.Set("X-Gately-Plugin-Token", secret)
			pr.Out.Header.Set("X-Forwarded-Host", r.Host)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.Error(w, http.StatusBadGateway, "插件服务不可用：%v", err)
		},
	}
	proxy.ServeHTTP(w, r)
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// pipeLines 把插件进程的输出逐行写入模块日志（管理界面 /api/plugin/logs 可见）。
func (m *Module) pipeLines(id string, rc io.ReadCloser) {
	defer m.wg.Done()
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		m.logs.Add(logbuf.LevelInfo, "plugin:"+id, sc.Text())
	}
}
