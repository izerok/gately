package plugin

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
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
	r.HandleFunc("PUT /plugins/{id}/options", m.handleOptionsUpdate)
	r.HandleFunc("POST /plugins/{id}/upgrade", m.handleUpgrade)
	// 发行层（协议第 10 节）：registry 来源管理与按需取索引（无后台定时检查），
	// 以及三条安装路径。以下接口的写操作同样走框架统一鉴权（plugin:write）。
	r.HandleFunc("GET /registry/sources", m.handleRegistrySources)
	r.HandleFunc("POST /registry/sources", m.handleRegistrySourceCreate)
	r.HandleFunc("PUT /registry/sources/{id}", m.handleRegistrySourceUpdate)
	r.HandleFunc("DELETE /registry/sources/{id}", m.handleRegistrySourceDelete)
	r.HandleFunc("GET /registry/index", m.handleRegistryIndex)
	r.HandleFunc("POST /install/registry", m.handleInstallRegistry)
	r.HandleFunc("POST /install/local", m.handleInstallLocal)
	r.HandleFunc("POST /install/upload", m.handleInstallUpload)
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
	module.DocOf(r, "PUT /plugins/{id}/options", module.Doc{Summary: "修改插件选项（运行中的命令型插件会重启以注入新选项）", Req: map[string]any{}, Resp: View{}})
	module.DocOf(r, "POST /plugins/{id}/upgrade", module.Doc{Summary: "从 registry 升级插件（旧进程先停，保留密钥与选项）", Req: map[string]string{}, Resp: View{}})
	module.DocOf(r, "GET /registry/sources", module.Doc{Summary: "列出 registry 来源（含官方空源占位）", Resp: []RegistrySource{}})
	module.DocOf(r, "POST /registry/sources", module.Doc{Summary: "添加 registry 来源", Req: RegistrySource{}, Resp: RegistrySource{}})
	module.DocOf(r, "PUT /registry/sources/{id}", module.Doc{Summary: "修改 registry 来源", Req: RegistrySource{}, Resp: RegistrySource{}})
	module.DocOf(r, "DELETE /registry/sources/{id}", module.Doc{Summary: "删除 registry 来源", Resp: map[string]bool{}})
	module.DocOf(r, "GET /registry/index", module.Doc{Summary: "按需拉取指定来源的索引（附本地安装状态）", Resp: map[string]any{}})
	module.DocOf(r, "POST /install/registry", module.Doc{Summary: "从 registry 一键安装插件（仅命令型）", Req: map[string]string{}, Resp: map[string]any{}})
	module.DocOf(r, "POST /install/local", module.Doc{Summary: "从本地目录安装插件", Req: map[string]string{}, Resp: map[string]any{}})
	module.DocOf(r, "POST /install/upload", module.Doc{Summary: "上传 zip 包安装插件", Resp: map[string]any{}})
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
	// 安装元数据与选项不走通用编辑：来源与安装目录是安装动作的产物，
	// 选项走专用接口（改选项会重启运行中的命令型插件以注入新值）。
	in.Source = p.Source
	in.InstallDir = p.InstallDir
	in.Options = p.Options
	in.Capabilities = p.Capabilities
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
	p, rt, found := m.find(r.PathValue("id"))
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
	// 安装文件默认保留；管理员勾选时一并删除（仅限配置目录下的安装目录）。
	filesDeleted := false
	if q := r.URL.Query().Get("deleteFiles"); q == "1" || q == "true" {
		filesDeleted = m.removeInstallDirLocked(p)
	}
	httpx.OK(w, map[string]any{"ok": true, "filesDeleted": filesDeleted})
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
	// 插件页由管理后台同源 iframe 展示：去掉全局安全中间件（securityHeaders）
	// 预先写上的 X-Frame-Options: DENY，否则浏览器拒绝在 iframe 里显示。
	// 只影响本反代接口的响应（含未运行等错误响应，iframe 内才能看到错误文案）；
	// 其他接口的 DENY 不受影响。注意 ModifyResponse 只能改上游响应头，
	// 中间件预置的头必须在这里从下游 ResponseWriter 上删掉。
	w.Header().Del("X-Frame-Options")
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
		ModifyResponse: func(resp *http.Response) error {
			// 插件服务自身也可能带 X-Frame-Options（例如默认 DENY 的框架）；
			// 反代只透传内容，帧策略由管理后台的同源 iframe 设计决定。
			resp.Header.Del("X-Frame-Options")
			return nil
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

// ---- 发行层接口（协议第 10 节）----
// 网络拉取（索引、安装包）与解压在加锁前完成，避免长耗时阻塞健康检查；
// 只有落盘注册的 installLocked 一段持有 m.mu。

func (m *Module) handleRegistrySources(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	httpx.OK(w, m.sourcesLocked())
}

func (m *Module) handleRegistrySourceCreate(w http.ResponseWriter, r *http.Request) {
	var in RegistrySource
	if err := httpx.ReadJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.ID == "" {
		in.ID = m.nextSourceIDLocked()
	}
	if err := in.validate(); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if _, stored := m.findStoredSourceLocked(in.ID); stored != nil {
		httpx.Error(w, http.StatusBadRequest, "来源 ID %q 已存在", in.ID)
		return
	}
	ns := in
	m.cfg.RegistrySources = append(m.cfg.RegistrySources, &ns)
	if err := m.saveLocked(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	httpx.OK(w, ns)
}

func (m *Module) handleRegistrySourceUpdate(w http.ResponseWriter, r *http.Request) {
	var in RegistrySource
	if err := httpx.ReadJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := r.PathValue("id")
	in.ID = id
	if err := in.validate(); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if idx, _ := m.findStoredSourceLocked(id); idx >= 0 {
		ns := in
		m.cfg.RegistrySources[idx] = &ns
	} else if id == "official" {
		// 官方占位源首次配置：由占位转正
		ns := in
		m.cfg.RegistrySources = append(m.cfg.RegistrySources, &ns)
	} else {
		httpx.Error(w, http.StatusNotFound, "来源不存在")
		return
	}
	if err := m.saveLocked(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	httpx.OK(w, in)
}

func (m *Module) handleRegistrySourceDelete(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := r.PathValue("id")
	if idx, _ := m.findStoredSourceLocked(id); idx >= 0 {
		m.cfg.RegistrySources = append(m.cfg.RegistrySources[:idx], m.cfg.RegistrySources[idx+1:]...)
		if err := m.saveLocked(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "%v", err)
			return
		}
	} else if id != "official" {
		httpx.Error(w, http.StatusNotFound, "来源不存在")
		return
	}
	// 删除官方占位源只是让它回到未配置状态（下次列表仍显示占位），无需落盘。
	httpx.OK(w, map[string]bool{"ok": true})
}

func (m *Module) handleRegistryIndex(w http.ResponseWriter, r *http.Request) {
	sourceID := strings.TrimSpace(r.URL.Query().Get("source"))
	if sourceID == "" {
		httpx.Error(w, http.StatusBadRequest, "请指定来源 ID")
		return
	}
	m.mu.Lock()
	src := m.findSourceLocked(sourceID)
	m.mu.Unlock()
	if src == nil {
		httpx.Error(w, http.StatusNotFound, "来源不存在")
		return
	}
	idx, err := m.fetchIndex(src)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "%v", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	views := make([]RegistryEntryView, 0, len(idx.Plugins))
	for _, e := range idx.Plugins {
		v := RegistryEntryView{RegistryEntry: e}
		if p, _, found := m.find(e.ID); found {
			v.InstalledVersion = p.Version
			if cmp, ok := comparePluginVersions(e.Version, p.Version); ok && cmp > 0 {
				v.Upgradable = true
			}
		}
		views = append(views, v)
	}
	httpx.OK(w, map[string]any{"source": src, "name": idx.Name, "plugins": views})
}

// installStaged 在加锁后安装 staging 包；失败时删除 staging，
// 成功时 staging 已被 installLocked 移到安装目录。
func (m *Module) installStaged(w http.ResponseWriter, staging, source string, upgrade bool) {
	m.mu.Lock()
	v, secret, err := m.installLocked(staging, source, upgrade)
	m.mu.Unlock()
	if err != nil {
		_ = os.RemoveAll(staging)
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if upgrade {
		httpx.OK(w, map[string]any{"plugin": v})
		return
	}
	httpx.OK(w, map[string]any{"plugin": v, "secret": secret})
}

func (m *Module) handleInstallRegistry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SourceID string `json:"sourceId"`
		PluginID string `json:"pluginId"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	in.SourceID = strings.TrimSpace(in.SourceID)
	in.PluginID = strings.TrimSpace(in.PluginID)
	if in.SourceID == "" || in.PluginID == "" {
		httpx.Error(w, http.StatusBadRequest, "请指定来源与插件 ID")
		return
	}
	m.mu.Lock()
	src := m.findSourceLocked(in.SourceID)
	m.mu.Unlock()
	if src == nil {
		httpx.Error(w, http.StatusNotFound, "来源不存在")
		return
	}
	idx, err := m.fetchIndex(src)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "%v", err)
		return
	}
	var entry *RegistryEntry
	for i := range idx.Plugins {
		if idx.Plugins[i].ID == in.PluginID {
			entry = &idx.Plugins[i]
			break
		}
	}
	if entry == nil {
		httpx.Error(w, http.StatusNotFound, "索引中没有该插件")
		return
	}
	if entry.Kind == KindURL {
		httpx.Error(w, http.StatusBadRequest, "URL 型插件请手动注册，registry 安装只面向命令型插件")
		return
	}
	data, err := m.downloadPackage(src, entry.DownloadURL)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "%v", err)
		return
	}
	staging, err := m.stageZip(data, entry.ID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	m.installStaged(w, staging, "registry:"+src.ID, false)
}

func (m *Module) handleInstallLocal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dir string `json:"dir"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	dir := strings.TrimSpace(in.Dir)
	if dir == "" {
		httpx.Error(w, http.StatusBadRequest, "请指定本地目录")
		return
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		httpx.Error(w, http.StatusBadRequest, "本地目录不存在或不是目录")
		return
	}
	staging, err := m.stageDir(dir, "local")
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	m.installStaged(w, staging, "local", false)
}

func (m *Module) handleInstallUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPackageBytes+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httpx.Error(w, http.StatusBadRequest, "读取上传文件失败：%v", err)
		return
	}
	fhs := r.MultipartForm.File["file"]
	if len(fhs) != 1 {
		httpx.Error(w, http.StatusBadRequest, "请上传一个 zip 包")
		return
	}
	f, err := fhs[0].Open()
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "读取上传文件失败：%v", err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPackageBytes+1))
	_ = f.Close()
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "读取上传文件失败：%v", err)
		return
	}
	if len(data) > maxPackageBytes {
		httpx.Error(w, http.StatusBadRequest, "安装包超过 %d MB", maxPackageBytes>>20)
		return
	}
	staging, err := m.stageZip(data, "upload")
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	m.installStaged(w, staging, "upload", false)
}

func (m *Module) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SourceID string `json:"sourceId"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	id := r.PathValue("id")
	m.mu.Lock()
	p, _, found := m.find(id)
	// 需要的字段在锁内复制出来，后续网络请求在锁外进行。
	installedVersion, installDir, sourceID := "", "", strings.TrimSpace(in.SourceID)
	if found {
		installedVersion, installDir = p.Version, p.InstallDir
		if sourceID == "" {
			// 未指定来源时，沿用安装时的 registry 来源
			if rest, ok := strings.CutPrefix(p.Source, "registry:"); ok {
				sourceID = rest
			}
		}
	}
	m.mu.Unlock()
	if !found {
		httpx.Error(w, http.StatusNotFound, "插件不存在")
		return
	}
	if installDir == "" {
		httpx.Error(w, http.StatusBadRequest, "手动注册的插件无法通过 registry 升级")
		return
	}
	if sourceID == "" {
		httpx.Error(w, http.StatusBadRequest, "请指定 registry 来源")
		return
	}
	m.mu.Lock()
	src := m.findSourceLocked(sourceID)
	m.mu.Unlock()
	if src == nil {
		httpx.Error(w, http.StatusNotFound, "来源不存在")
		return
	}
	idx, err := m.fetchIndex(src)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "%v", err)
		return
	}
	var entry *RegistryEntry
	for i := range idx.Plugins {
		if idx.Plugins[i].ID == id {
			entry = &idx.Plugins[i]
			break
		}
	}
	if entry == nil {
		httpx.Error(w, http.StatusNotFound, "索引中没有该插件")
		return
	}
	if cmp, ok := comparePluginVersions(entry.Version, installedVersion); !ok || cmp <= 0 {
		httpx.Error(w, http.StatusBadRequest, "已是最新版本（索引 %q，本地 %q）", entry.Version, installedVersion)
		return
	}
	data, err := m.downloadPackage(src, entry.DownloadURL)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "%v", err)
		return
	}
	staging, err := m.stageZip(data, id)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	m.installStaged(w, staging, "registry:"+src.ID, true)
}

func (m *Module) handleOptionsUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Options map[string]any `json:"options"`
	}
	if err := httpx.ReadJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "%v", err)
		return
	}
	if in.Options == nil {
		httpx.Error(w, http.StatusBadRequest, "选项须为 JSON 对象")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, rt, found := m.find(r.PathValue("id"))
	if !found {
		httpx.Error(w, http.StatusNotFound, "插件不存在")
		return
	}
	p.Options = in.Options
	if err := m.saveLocked(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "%v", err)
		return
	}
	// 运行中的命令型插件重启一次，让新选项经 PLUGIN_OPTIONS 生效；
	// URL 型插件的选项只作记录（协议第 10.4 节的明确局限）。
	if p.Kind == KindCommand && rt.state == StateRunning {
		m.stopLocked(rt)
		rt.manualStop = false
		rt.restarts = 0
		m.startLocked(rt)
	}
	httpx.OK(w, m.viewLocked(p))
}
