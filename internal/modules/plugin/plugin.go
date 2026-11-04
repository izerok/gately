// Package plugin 实现 Gately 插件中心（路线 B：外部插件协议 v1）。
//
// 插件是一个独立运行的 HTTP 服务，有两种形态：
//   - command：由 Gately 监管启动的外部可执行文件，监听 Gately 分配的回环端口；
//   - url：已经在运行的服务（例如 Docker 容器暴露的端口），Gately 只做反代与健康检查。
//
// Gately 不执行插件代码之外的任何逻辑：插件页面经 /api/plugin/pages/<id>/ 反代，
// 浏览器凭登录会话访问；反代时剥离 Cookie/Authorization，改以 X-Gately-* 头向插件
// 表明身份。插件若需调用 Gately 自身 API，须用管理员另行创建的 API 令牌
// （权限按插件声明的 scopes 授予），与普通 API 客户端无异。
//
// 信任边界（v1 明确不解决，见 docs/plugin-protocol-v1.md）：
//   - 命令插件以 Gately 进程的操作系统用户身份运行，与 Gately 同权；
//   - 插件页面与管理界面同源，页面脚本可凭当前会话调用 Gately API。
//
// 因此只安装完全信任的插件；沙箱、独立源与细粒度插件权限属于后续版本。
package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangwanjie/gately/internal/config"
	"github.com/wangwanjie/gately/internal/logbuf"
	"github.com/wangwanjie/gately/internal/module"
)

// ID 是模块标识，同时作为配置文件名和 API 前缀。
const ID = "plugin"

// KindCommand 由 Gately 监管启动外部进程；KindURL 只反代已有服务。
const (
	KindCommand = "command"
	KindURL     = "url"
)

// State* 为插件运行状态。
const (
	StateStopped  = "stopped"
	StateRunning  = "running"
	StateError    = "error"
	StateStarting = "starting"
)

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Plugin 是一条插件注册记录（持久化部分）。
type Plugin struct {
	ID string `json:"id"`
	// Name 为管理界面与页面标题显示名
	Name string `json:"name"`
	// Version / Description / Icon 由插件作者声明，仅展示用
	Version     string `json:"version"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	// Kind 为 command 或 url
	Kind string `json:"kind"`
	// Command 为 command 型插件的可执行文件绝对路径
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// Env 为注入给 command 型插件的额外环境变量（PLUGIN_PORT 等由框架追加）
	Env map[string]string `json:"env"`
	// WorkDir 为 command 型插件的工作目录，留空为 Gately 工作目录
	WorkDir string `json:"workDir"`
	// URL 为 url 型插件的服务地址，如 http://127.0.0.1:8080
	URL string `json:"url"`
	// Port 为 command 型插件的监听端口，0 表示由 Gately 分配空闲回环端口
	Port int `json:"port"`
	// HealthPath 为健康检查路径，留空表示只做 TCP 连通检查
	HealthPath string `json:"healthPath"`
	// Enabled 为 false 时不启动、不反代
	Enabled bool `json:"enabled"`
	// Scopes 为插件声明的、调用 Gately API 时需要的权限（仅声明与展示，
	// 实际授权仍由管理员创建 API 令牌时授予，见协议文档）
	Scopes []string `json:"scopes"`
	// Options 为插件选项（见协议文档第 10.4 节）：命令型插件启动时以
	// PLUGIN_OPTIONS 环境变量注入；URL 型插件只作记录，不投递。
	Options map[string]any `json:"options"`
	// Capabilities 为插件包声明的能力（清单 capabilities）；本版只有 pages，
	// 手动注册的插件留空。
	Capabilities []string `json:"capabilities"`
	// Source 为安装来源：手动注册留空；registry 安装为 "registry:<来源 ID>"，
	// 本地目录安装为 "local"，上传安装为 "upload"。
	Source string `json:"source"`
	// InstallDir 为安装包所在目录（手动注册与 URL 型插件留空）；
	// 卸载时管理员可勾选一并删除该目录。
	InstallDir string `json:"installDir"`
	// Secret 为反代时注入的共享密钥，创建时生成；列表与详情接口不返回它
	Secret string `json:"-"`
}

// Validate 校验注册字段。
func (p *Plugin) Validate() error {
	if !idRe.MatchString(p.ID) {
		return errors.New("插件 ID 须以小写字母开头，仅含小写字母、数字、下划线与连字符，长度不超过 32")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("插件名称不能为空")
	}
	switch p.Kind {
	case KindCommand:
		if p.Command == "" || !strings.HasPrefix(p.Command, "/") {
			return errors.New("命令型插件须填写可执行文件绝对路径")
		}
	case KindURL:
		u, err := url.Parse(strings.TrimSpace(p.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("URL 型插件须填写合法的 http(s) 服务地址")
		}
		p.URL = strings.TrimRight(u.String(), "/")
	default:
		return errors.New("插件类型须为 command 或 url")
	}
	if p.Port < 0 || p.Port > 65535 {
		return errors.New("端口须在 0-65535 之间，0 表示自动分配")
	}
	if p.HealthPath != "" && !strings.HasPrefix(p.HealthPath, "/") {
		return errors.New("健康检查路径须以 / 开头")
	}
	return nil
}

// Status 为插件运行状态（管理界面轮询用）。
type Status struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	PID        int    `json:"pid"`
	Port       int    `json:"port"`
	Target     string `json:"target"`
	LastHealth string `json:"lastHealth"`
	LastError  string `json:"lastError"`
	Restarts   int    `json:"restarts"`
	StartedAt  string `json:"startedAt"`
}

// View 为去掉密钥后的插件展示结构。
type View struct {
	Plugin
	Status Status `json:"status"`
}

type fileConfig struct {
	// 用指针切片：新增/删除插件时已有 runtime 的指针不因底层数组重分配而失效。
	Plugins []*Plugin `json:"plugins"`
	// RegistrySources 为管理员配置的 registry 索引来源（见 distribution.go）；
	// 官方源在本版以空源占位：未配置时列表接口自动补一条 URL 为空的官方源。
	RegistrySources []*RegistrySource `json:"registrySources"`
}

// runtime 为单个插件的运行时状态。
type runtime struct {
	plug     *Plugin
	cmd      *exec.Cmd
	done     chan struct{}
	target   *url.URL
	state    string
	pid      int
	port     int
	fails    int
	restarts int
	lastErr  string
	lastOK   time.Time
	started  time.Time
	// manualStop 为 true 时健康检查不再自动拉起
	manualStop bool
}

// Module 实现 module.Module。
type Module struct {
	store  *config.Store
	logs   *logbuf.Buffer
	mu     sync.Mutex
	cfg    fileConfig
	rts    map[string]*runtime
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// httpClientFn 由框架注入（module.HTTPClientAware），用于 registry 索引
	// 与安装包下载，遵循“更新代理”设置；未注入时用 http.DefaultClient。
	httpClientFn func() *http.Client
}

func New() *Module { return &Module{rts: map[string]*runtime{}} }

func (m *Module) ID() string    { return ID }
func (m *Module) Title() string { return "插件中心" }

func (m *Module) Init(env module.Env) error {
	m.store = env.Store
	m.logs = env.Logs
	_, err := m.store.Load(ID, &m.cfg)
	if err != nil {
		return err
	}
	for _, pl := range m.cfg.Plugins {
		m.rts[pl.ID] = &runtime{plug: pl, state: StateStopped}
	}
	return nil
}

func (m *Module) Summary() []module.SummaryItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	running, total := 0, len(m.cfg.Plugins)
	for _, rt := range m.rts {
		if rt.state == StateRunning {
			running++
		}
	}
	return []module.SummaryItem{{Label: "插件", Value: fmt.Sprintf("%d/%d 运行中", running, total), Level: "info"}}
}

func (m *Module) Start(ctx context.Context) error {
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.mu.Lock()
	for _, rt := range m.rts {
		if rt.plug.Enabled {
			m.startLocked(rt)
		}
	}
	m.mu.Unlock()
	m.wg.Add(1)
	go m.healthLoop()
	return nil
}

func (m *Module) Stop() error {
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	for _, rt := range m.rts {
		m.stopLocked(rt)
	}
	m.mu.Unlock()
	m.wg.Wait()
	return nil
}

func (m *Module) saveLocked() error { return m.store.Save(ID, m.cfg) }

// SetHTTPClient 实现 module.HTTPClientAware：框架在 Init 之前注入，
// registry 相关外网请求经该客户端发出（遵循更新代理设置）。
func (m *Module) SetHTTPClient(fn func() *http.Client) { m.httpClientFn = fn }

func (m *Module) httpClient() *http.Client {
	if m.httpClientFn != nil {
		if c := m.httpClientFn(); c != nil {
			return c
		}
	}
	return http.DefaultClient
}

func (m *Module) find(id string) (*Plugin, *runtime, bool) {
	for _, pl := range m.cfg.Plugins {
		if pl.ID == id {
			return pl, m.rts[id], true
		}
	}
	return nil, nil, false
}

func (m *Module) viewLocked(p *Plugin) View {
	rt := m.rts[p.ID]
	v := View{Plugin: *p}
	v.Secret = ""
	if rt != nil {
		v.Status = Status{
			ID: p.ID, State: rt.state, PID: rt.pid, Port: rt.port,
			LastError: rt.lastErr, Restarts: rt.restarts,
		}
		if rt.target != nil {
			v.Status.Target = rt.target.String()
		}
		if !rt.lastOK.IsZero() {
			v.Status.LastHealth = rt.lastOK.Format(time.RFC3339)
		}
		if !rt.started.IsZero() {
			v.Status.StartedAt = rt.started.Format(time.RFC3339)
		}
	}
	return v
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// allocPort 分配一个空闲回环端口（分配与监听之间存在竞态，插件启动后立即
// 绑定；分配失败或被抢占时启动报错并进入退避重试）。
func allocPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startLocked 启动一个插件（调用方持有 m.mu）。
func (m *Module) startLocked(rt *runtime) {
	p := rt.plug
	rt.manualStop = false
	rt.state = StateStarting
	rt.lastErr = ""
	if p.Kind == KindURL {
		u, err := url.Parse(p.URL)
		if err != nil {
			rt.state, rt.lastErr = StateError, err.Error()
			return
		}
		rt.target = u
		if port, _ := strconv.Atoi(u.Port()); port != 0 {
			rt.port = port
		}
		rt.state = StateRunning
		rt.started = time.Now()
		m.logs.Add(logbuf.LevelInfo, "plugin:"+p.ID, "URL 型插件已登记，反代目标 "+u.String())
		return
	}
	port := p.Port
	if port == 0 {
		free, err := allocPort()
		if err != nil {
			rt.state, rt.lastErr = StateError, "分配端口失败："+err.Error()
			return
		}
		port = free
	}
	cmd := exec.CommandContext(m.ctx, p.Command, p.Args...)
	cmd.Dir = p.WorkDir
	env := os.Environ()
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	// 选项以 JSON 注入 PLUGIN_OPTIONS（协议文档第 10.4 节；无选项时为 {}）。
	// 注入失败不可能来自 JSON 输入（选项只经 JSON 接口进出），失败时退回 {}。
	optionsJSON := "{}"
	if p.Options != nil {
		if b, err := json.Marshal(p.Options); err == nil {
			optionsJSON = string(b)
		}
	}
	env = append(env,
		"PLUGIN_PORT="+strconv.Itoa(port),
		"GATELY_PLUGIN_ID="+p.ID,
		"GATELY_PLUGIN_NAME="+p.Name,
		"PLUGIN_OPTIONS="+optionsJSON,
	)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		rt.state, rt.lastErr = StateError, err.Error()
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		rt.state, rt.lastErr = StateError, err.Error()
		return
	}
	if err := cmd.Start(); err != nil {
		rt.state, rt.lastErr = StateError, "启动失败："+err.Error()
		return
	}
	rt.cmd = cmd
	rt.done = make(chan struct{})
	rt.pid = cmd.Process.Pid
	rt.port = port
	rt.target = &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}
	rt.state = StateRunning
	rt.started = time.Now()
	m.logs.Add(logbuf.LevelInfo, "plugin:"+p.ID, fmt.Sprintf("命令型插件已启动，PID %d，端口 %d", rt.pid, port))
	m.wg.Add(1)
	go m.pipeLines(p.ID, stdout)
	m.wg.Add(1)
	go m.pipeLines(p.ID, stderr)
	// done 必须按值捕获：重启时 startLocked 会换一个新的 rt.done，
	// 旧进程的等待协程若按现值 close(rt.done)，会误关新通道，
	// 新进程退出时再关一次即 panic（升级/重启路径实测触发）。
	done := rt.done
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		_ = cmd.Wait()
		close(done)
		m.mu.Lock()
		defer m.mu.Unlock()
		if rt.cmd == cmd && !rt.manualStop {
			rt.state, rt.lastErr = StateError, "插件进程已退出"
			m.logs.Add(logbuf.LevelWarn, "plugin:"+p.ID, "插件进程已退出，健康检查将按退避重试拉起")
		}
	}()
}

// stopLocked 停止一个插件（调用方持有 m.mu）。
func (m *Module) stopLocked(rt *runtime) {
	rt.manualStop = true
	if rt.cmd != nil && rt.cmd.Process != nil {
		_ = rt.cmd.Process.Kill()
	}
	rt.cmd = nil
	rt.pid = 0
	rt.state = StateStopped
	rt.lastErr = ""
}

// healthLoop 每 30 秒检查一次运行中插件的健康状态。
func (m *Module) healthLoop() {
	defer m.wg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.checkAll()
		}
	}
}

var restartDelays = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second}

func (m *Module) checkAll() {
	m.mu.Lock()
	var retry []*runtime
	for _, rt := range m.rts {
		if rt.state != StateRunning || !rt.plug.Enabled || rt.manualStop {
			continue
		}
		if m.healthy(rt) {
			rt.fails = 0
			rt.lastOK = time.Now()
			continue
		}
		rt.fails++
		if rt.fails >= 3 {
			if rt.plug.Kind == KindURL {
				rt.state, rt.lastErr = StateError, "健康检查连续 3 次失败"
				m.logs.Add(logbuf.LevelWarn, "plugin:"+rt.plug.ID, "健康检查连续 3 次失败，已标记为异常（URL 型插件不自动重启）")
				continue
			}
			if rt.restarts >= len(restartDelays) {
				rt.state, rt.lastErr = StateError, "重启次数已耗尽，需手动启动"
				m.logs.Add(logbuf.LevelWarn, "plugin:"+rt.plug.ID, "重启次数已耗尽，停止自动拉起")
				continue
			}
			rt.fails = 0
			retry = append(retry, rt)
		}
	}
	m.mu.Unlock()
	for _, rt := range retry {
		delay := restartDelays[rt.restarts]
		m.logs.Add(logbuf.LevelInfo, "plugin:"+rt.plug.ID, fmt.Sprintf("%s 后尝试重启", delay))
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(delay):
		}
		m.mu.Lock()
		if rt.plug.Enabled && !rt.manualStop && rt.state == StateRunning {
			m.stopLocked(rt)
			rt.manualStop = false
			rt.restarts++
			m.startLocked(rt)
		}
		m.mu.Unlock()
	}
}

// healthy 检查插件是否健康：command 型先看进程是否存活，
// 再按 HealthPath 做 HTTP 检查（留空则只做 TCP 连通检查）。
func (m *Module) healthy(rt *runtime) bool {
	if rt.plug.Kind == KindCommand && rt.cmd != nil {
		select {
		case <-rt.done:
			return false
		default:
		}
	}
	if rt.target == nil {
		return false
	}
	host := rt.target.Host
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	if rt.plug.HealthPath == "" {
		return true
	}
	u := *rt.target
	u.Path = strings.TrimSuffix(u.Path, "/") + rt.plug.HealthPath
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < 400
}
