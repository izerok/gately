// 发行层（对齐 Magpie，协议文档第 10 节）：registry 索引、插件包安装 / 升级 /
// 卸载文件清理、选项管理。
//
// 设计要点：
//   - Registry 就是一份 JSON 索引，可由 HTTP(S) URL 或本地目录（内含 index.json）提供；
//     来源分官方源与社区源——这就是官方插件与第三方插件的区分，同一条安装路径，
//     不设白名单以外的特权。
//   - 官方源地址待官方插件发布时确定，本版先以空源占位：未配置时列表接口自动补
//     一条 URL 为空的官方源，绝不编造官方 URL；取索引时对空源明确报错。
//   - 信任边界不变：registry 本身须可信，本版不做签名校验，只装可信源。
//   - 版本检查只在管理界面打开 / 手动刷新时做：取索引是按需的，没有后台定时检查。
//   - 安装只面向命令型插件（自包含包）；URL 型插件仍手动注册。
package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wangwanjie/gately/internal/version"
)

// 安装包与索引的大小上限：防恶意 / 失误包撑满磁盘。
const (
	maxPackageBytes   = 64 << 20
	maxExtractedBytes = 256 << 20
	maxExtractedFiles = 2000
	maxIndexBytes     = 8 << 20
	indexTimeout      = 30 * time.Second
	downloadTimeout   = 120 * time.Second
)

// Manifest 为插件包根部的 plugin.json 清单：注册字段 + 发行字段。
type Manifest struct {
	Plugin
	// Entry 为命令型插件的包内相对可执行路径，安装后解析为绝对路径。
	Entry string `json:"entry"`
	// Options 为默认选项对象，安装后管理员可改。
	Options map[string]any `json:"options"`
	// Capabilities 为能力声明数组，本版只有 pages；hooks 预留未来。
	Capabilities []string `json:"capabilities"`
	// MinGatelyVersion 可选，声明最低 Gately 版本。
	MinGatelyVersion string `json:"minGatelyVersion"`
}

// validate 校验清单：命令型是唯一合法的包形态（URL 型插件无包可言，须手动注册）。
func (mf *Manifest) validate() error {
	if !idRe.MatchString(mf.ID) {
		return errors.New("插件 ID 须以小写字母开头，仅含小写字母、数字、下划线与连字符，长度不超过 32")
	}
	mf.Name = strings.TrimSpace(mf.Name)
	if mf.Name == "" {
		return errors.New("插件名称不能为空")
	}
	if mf.Kind != KindCommand {
		return errors.New("插件包只支持命令型插件，URL 型插件请手动注册")
	}
	if strings.TrimSpace(mf.Entry) == "" {
		return errors.New("清单缺少 entry（包内相对可执行路径）")
	}
	if filepath.IsAbs(mf.Entry) {
		return errors.New("entry 须为包内相对路径，不能是绝对路径")
	}
	if mf.Port < 0 || mf.Port > 65535 {
		return errors.New("端口须在 0-65535 之间，0 表示自动分配")
	}
	if mf.HealthPath != "" && !strings.HasPrefix(mf.HealthPath, "/") {
		return errors.New("健康检查路径须以 / 开头")
	}
	if len(mf.Capabilities) == 0 {
		mf.Capabilities = []string{"pages"}
	}
	for _, c := range mf.Capabilities {
		if c != "pages" {
			return fmt.Errorf("不支持的能力 %q：本版只有 pages（hooks 为未来预留）", c)
		}
	}
	return nil
}

// resolveEntry 把包内相对 entry 解析为目录内的绝对路径，拒绝绝对路径与逃出目录。
func resolveEntry(dir, entry string) (string, error) {
	if filepath.IsAbs(entry) || filepath.IsAbs(filepath.FromSlash(entry)) {
		return "", fmt.Errorf("entry %q 须为包内相对路径", entry)
	}
	clean := filepath.Clean(filepath.FromSlash(entry))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("entry %q 超出包目录", entry)
	}
	abs := filepath.Join(dir, clean)
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("entry %q 超出包目录", entry)
	}
	return abs, nil
}

// checkMinGatelyVersion 校验当前版本是否满足清单的最低版本要求；
// 当前版本无法解析（如 dev 构建）时不拦截。
func checkMinGatelyVersion(min string) error {
	min = strings.TrimSpace(min)
	if min == "" {
		return nil
	}
	cmp, ok := comparePluginVersions(version.Version, min)
	if !ok {
		return nil
	}
	if cmp < 0 {
		return fmt.Errorf("该插件要求 Gately %s 或更高，当前为 %s", min, version.Version)
	}
	return nil
}

// parsePluginVersion 解析版本号：可选 v 前缀、数字点分核心、可选 -预发布，
// +构建元数据忽略。核心任一段非数字即视为无法解析。
func parsePluginVersion(s string) (nums []int, pre string, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return nil, "", false
	}
	if i := strings.Index(s, "+"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "-"); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	if s == "" {
		return nil, "", false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return nil, "", false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, "", false
		}
		nums = append(nums, n)
	}
	return nums, pre, true
}

// comparePluginVersions 比较版本号：a 新返回 1，相同返回 0，旧返回 -1；
// 任一无法解析时 ok 为 false（调用方按“不可比较”处理，不判定可升级）。
func comparePluginVersions(a, b string) (int, bool) {
	na, prea, okA := parsePluginVersion(a)
	nb, preb, okB := parsePluginVersion(b)
	if !okA || !okB {
		return 0, false
	}
	n := len(na)
	if len(nb) > n {
		n = len(nb)
	}
	for i := 0; i < n; i++ {
		va, vb := 0, 0
		if i < len(na) {
			va = na[i]
		}
		if i < len(nb) {
			vb = nb[i]
		}
		if va != vb {
			if va > vb {
				return 1, true
			}
			return -1, true
		}
	}
	switch {
	case prea == preb:
		return 0, true
	case prea == "":
		return 1, true
	case preb == "":
		return -1, true
	case prea > preb:
		return 1, true
	}
	return -1, true
}

// RegistrySource 为一个 registry 索引来源：URL 与本地目录二选一。
type RegistrySource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind 为 official（官方）或 community（社区）
	Kind string `json:"kind"`
	// URL 为 http(s) 索引地址；本地目录源留空
	URL string `json:"url"`
	// Dir 为本地目录（内含 index.json）；URL 源留空
	Dir string `json:"dir"`
}

func (s *RegistrySource) validate() error {
	if s.ID != "" && !idRe.MatchString(s.ID) {
		return errors.New("来源 ID 格式非法")
	}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return errors.New("来源名称不能为空")
	}
	if s.Kind != "official" && s.Kind != "community" {
		return errors.New("来源类型须为 official 或 community")
	}
	s.URL = strings.TrimSpace(s.URL)
	s.Dir = strings.TrimSpace(s.Dir)
	if (s.URL == "") == (s.Dir == "") {
		return errors.New("来源须二选一：索引 URL 或本地目录")
	}
	if s.URL != "" {
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("索引 URL 须为合法的 http(s) 地址")
		}
	}
	if s.Dir != "" {
		st, err := os.Stat(s.Dir)
		if err != nil {
			return fmt.Errorf("本地目录不存在：%w", err)
		}
		if !st.IsDir() {
			return errors.New("本地目录源须指向一个目录")
		}
	}
	return nil
}

// RegistryIndex 为 registry 索引文件内容。
type RegistryIndex struct {
	Name    string          `json:"name"`
	Plugins []RegistryEntry `json:"plugins"`
}

// RegistryEntry 为索引中的一条：清单字段 + 下载地址。
type RegistryEntry struct {
	Manifest
	DownloadURL string `json:"downloadUrl"`
}

// RegistryEntryView 为返回给管理界面的索引条目，附本地安装状态。
type RegistryEntryView struct {
	RegistryEntry
	InstalledVersion string `json:"installedVersion"`
	Upgradable       bool   `json:"upgradable"`
}

// sourcesLocked 返回全部来源：已存记录 + 未配置时的官方空源占位。
// 调用方持有 m.mu；返回的是副本，修改不影响存储。
func (m *Module) sourcesLocked() []*RegistrySource {
	out := make([]*RegistrySource, 0, len(m.cfg.RegistrySources)+1)
	hasOfficial := false
	for _, s := range m.cfg.RegistrySources {
		cp := *s
		out = append(out, &cp)
		if s.Kind == "official" {
			hasOfficial = true
		}
	}
	if !hasOfficial {
		out = append([]*RegistrySource{{ID: "official", Name: "官方", Kind: "official"}}, out...)
	}
	return out
}

// findSourceLocked 按 ID 找来源（含官方占位源）；调用方持有 m.mu。
func (m *Module) findSourceLocked(id string) *RegistrySource {
	for _, s := range m.sourcesLocked() {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// findStoredSourceLocked 按 ID 找已存记录（不含占位源），返回其在切片中的下标；调用方持有 m.mu。
func (m *Module) findStoredSourceLocked(id string) (int, *RegistrySource) {
	for i, s := range m.cfg.RegistrySources {
		if s.ID == id {
			return i, s
		}
	}
	return -1, nil
}

// nextSourceIDLocked 生成未占用的来源 ID；调用方持有 m.mu。
func (m *Module) nextSourceIDLocked() string {
	used := map[string]bool{"official": true}
	for _, s := range m.cfg.RegistrySources {
		used[s.ID] = true
	}
	for i := 1; ; i++ {
		id := "src" + strconv.Itoa(i)
		if !used[id] {
			return id
		}
	}
}

// fetchIndex 按需拉取一个来源的索引（管理界面打开 / 手动刷新时调用，
// 没有后台定时检查）。本地目录源读取目录内的 index.json。
func (m *Module) fetchIndex(src *RegistrySource) (*RegistryIndex, error) {
	var data []byte
	switch {
	case src.Dir != "":
		b, err := os.ReadFile(filepath.Join(src.Dir, "index.json"))
		if err != nil {
			return nil, fmt.Errorf("读取本地索引失败：%w", err)
		}
		data = b
	case src.URL != "":
		ctx, cancel := context.WithTimeout(context.Background(), indexTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := m.httpClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("拉取索引失败：%w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("拉取索引失败：HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes+1))
		if err != nil {
			return nil, fmt.Errorf("读取索引失败：%w", err)
		}
		data = b
	default:
		if src.Kind == "official" {
			return nil, errors.New("官方索引地址尚未发布（本版以空源占位），请先配置官方源或添加社区源")
		}
		return nil, errors.New("来源未配置索引地址")
	}
	if len(data) > maxIndexBytes {
		return nil, fmt.Errorf("索引超过 %d MB", maxIndexBytes>>20)
	}
	var idx RegistryIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("解析索引失败：%w", err)
	}
	return &idx, nil
}

// downloadPackage 下载（或读取）一个 registry 条目的安装包：
// 本地目录源的 downloadUrl 可为源目录内相对路径或绝对路径；
// URL 源的 downloadUrl 必须为 http(s) 地址。
func (m *Module) downloadPackage(src *RegistrySource, downloadURL string) ([]byte, error) {
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		return nil, errors.New("索引条目缺少 downloadUrl")
	}
	if src.Dir != "" && !strings.HasPrefix(downloadURL, "http://") && !strings.HasPrefix(downloadURL, "https://") {
		var p string
		if filepath.IsAbs(downloadURL) {
			p = downloadURL
		} else {
			p = filepath.Join(src.Dir, downloadURL)
			rel, err := filepath.Rel(src.Dir, p)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				return nil, errors.New("downloadUrl 超出来源目录")
			}
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("读取安装包失败：%w", err)
		}
		if len(data) > maxPackageBytes {
			return nil, fmt.Errorf("安装包超过 %d MB", maxPackageBytes>>20)
		}
		return data, nil
	}
	u, err := url.Parse(downloadURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("downloadUrl 须为 http(s) 地址（本地目录源可用相对路径）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载安装包失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载安装包失败：HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPackageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取安装包失败：%w", err)
	}
	if len(data) > maxPackageBytes {
		return nil, fmt.Errorf("安装包超过 %d MB", maxPackageBytes>>20)
	}
	return data, nil
}

// stageZip 把 zip 包解到插件目录下的临时 staging 目录；调用方负责
// 成功安装后由 installLocked 移走、失败时删除。解压时防 Zip-Slip、
// 拒绝符号链接、限制文件数与总体积；包内必须含根部 plugin.json。
func (m *Module) stageZip(data []byte, id string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("不是有效的 zip 包：%w", err)
	}
	base := filepath.Join(m.store.Dir(), "plugins")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(base, ".tmp-"+id+"-")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		_ = os.RemoveAll(staging)
		return "", err
	}
	var total int64
	for i, f := range zr.File {
		if i+1 > maxExtractedFiles {
			return fail(errors.New("包内文件过多"))
		}
		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fail(fmt.Errorf("包内含符号链接 %q，不予解压", f.Name))
		}
		name := filepath.FromSlash(f.Name)
		if filepath.IsAbs(name) {
			return fail(fmt.Errorf("包内路径 %q 为绝对路径，不予解压", f.Name))
		}
		target := filepath.Join(staging, name)
		rel, err := filepath.Rel(staging, target)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fail(fmt.Errorf("包内路径 %q 试图逃出包目录", f.Name))
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fail(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fail(err)
		}
		rc, err := f.Open()
		if err != nil {
			return fail(err)
		}
		w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			_ = rc.Close()
			return fail(err)
		}
		n, copyErr := io.Copy(w, io.LimitReader(rc, maxExtractedBytes-total+1))
		total += n
		_ = rc.Close()
		_ = w.Close()
		if copyErr != nil {
			return fail(copyErr)
		}
		if total > maxExtractedBytes {
			return fail(errors.New("解压后体积过大"))
		}
		if f.Mode()&0o111 != 0 {
			_ = os.Chmod(target, 0o755)
		}
	}
	if _, err := os.Stat(filepath.Join(staging, "plugin.json")); err != nil {
		return fail(errors.New("包内缺少根部 plugin.json"))
	}
	return staging, nil
}

// stageDir 把本地目录包复制到临时 staging 目录（源目录原样保留）；
// 符号链接不跟随、直接跳过，体积与文件数同样受限。
func (m *Module) stageDir(srcDir, id string) (string, error) {
	if _, err := os.Stat(filepath.Join(srcDir, "plugin.json")); err != nil {
		return "", errors.New("该目录内缺少 plugin.json")
	}
	base := filepath.Join(m.store.Dir(), "plugins")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(base, ".tmp-"+id+"-")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		_ = os.RemoveAll(staging)
		return "", err
	}
	var total int64
	count := 0
	err = filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(staging, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		count++
		if count > maxExtractedFiles {
			return errors.New("包内文件过多")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > maxExtractedBytes {
			return errors.New("包体积过大")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
	if err != nil {
		return fail(err)
	}
	return staging, nil
}

// mergeOptions 合并选项：升级时保留管理员已改的值，只补新清单带来的默认值。
func mergeOptions(old, def map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range old {
		out[k] = v
	}
	for k, v := range def {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}

// installLocked 把 staging 目录里的插件包安装为一条注册记录。
// 调用方持有 m.mu；网络下载与解压在加锁前完成，避免长耗时阻塞健康检查。
// staging 成功时被 rename 到安装目录，失败时由调用方删除。
// upgrade 为 true 时为升级：旧进程先停，保留密钥与启用状态，选项只补新增默认值。
func (m *Module) installLocked(staging, source string, upgrade bool) (View, string, error) {
	data, err := os.ReadFile(filepath.Join(staging, "plugin.json"))
	if err != nil {
		return View{}, "", fmt.Errorf("读取 plugin.json 失败：%w", err)
	}
	var mf Manifest
	if err := json.Unmarshal(data, &mf); err != nil {
		return View{}, "", fmt.Errorf("解析 plugin.json 失败：%w", err)
	}
	if err := mf.validate(); err != nil {
		return View{}, "", err
	}
	if err := checkMinGatelyVersion(mf.MinGatelyVersion); err != nil {
		return View{}, "", err
	}
	installBase := filepath.Join(m.store.Dir(), "plugins")
	if err := os.MkdirAll(installBase, 0o700); err != nil {
		return View{}, "", err
	}
	installDir := filepath.Join(installBase, mf.ID)

	var old *Plugin
	var oldRT *runtime
	if upgrade {
		p, rt, found := m.find(mf.ID)
		if !found {
			return View{}, "", fmt.Errorf("插件 %q 未安装，无法升级", mf.ID)
		}
		old, oldRT = p, rt
		m.stopLocked(rt) // 旧进程先停（协议第 10.3 节）
	} else if _, _, found := m.find(mf.ID); found {
		return View{}, "", fmt.Errorf("插件 %q 已存在，请先卸载或升级", mf.ID)
	}
	stagingEntry, err := resolveEntry(staging, mf.Entry)
	if err != nil {
		return View{}, "", err
	}
	if _, err := os.Stat(stagingEntry); err != nil {
		return View{}, "", fmt.Errorf("清单 entry %q 在包内不存在：%w", mf.Entry, err)
	}
	if err := os.Chmod(stagingEntry, 0o755); err != nil {
		return View{}, "", err
	}
	// 落盘：先清旧目录，再把 staging 移过去（同父目录，rename 不跨文件系统）。
	if err := os.RemoveAll(installDir); err != nil {
		return View{}, "", fmt.Errorf("清理旧安装目录失败：%w", err)
	}
	if err := os.Rename(staging, installDir); err != nil {
		return View{}, "", fmt.Errorf("安装包落盘失败：%w", err)
	}
	entryAbs, err := resolveEntry(installDir, mf.Entry)
	if err != nil {
		return View{}, "", err
	}
	np := Plugin{
		ID: mf.ID, Name: mf.Name, Version: mf.Version, Description: mf.Description,
		Icon: mf.Icon, Kind: KindCommand, Command: entryAbs, Args: mf.Args, Env: mf.Env,
		WorkDir: installDir, Port: mf.Port, HealthPath: mf.HealthPath, Scopes: mf.Scopes,
		Capabilities: mf.Capabilities, Source: source, InstallDir: installDir,
	}
	if upgrade {
		np.Secret = old.Secret
		np.Enabled = old.Enabled
		np.Options = mergeOptions(old.Options, mf.Options)
	} else {
		secret, err := newSecret()
		if err != nil {
			return View{}, "", err
		}
		np.Secret = secret
		np.Enabled = true
		np.Options = mf.Options
		if np.Options == nil {
			np.Options = map[string]any{}
		}
	}
	if upgrade {
		for i, pl := range m.cfg.Plugins {
			if pl.ID == mf.ID {
				m.cfg.Plugins[i] = &np
				break
			}
		}
		oldRT.plug = &np
	} else {
		m.cfg.Plugins = append(m.cfg.Plugins, &np)
		m.rts[mf.ID] = &runtime{plug: &np, state: StateStopped}
	}
	if err := m.saveLocked(); err != nil {
		return View{}, "", err
	}
	rt := m.rts[mf.ID]
	if np.Enabled {
		m.startLocked(rt)
	}
	return m.viewLocked(&np), np.Secret, nil
}

// removeInstallDirLocked 删除一条插件记录的安装目录（仅限配置目录下的
// plugins 子目录，防止误伤手动注册记录）；调用方持有 m.mu。
func (m *Module) removeInstallDirLocked(p *Plugin) bool {
	if p.InstallDir == "" {
		return false
	}
	base := filepath.Join(m.store.Dir(), "plugins") + string(filepath.Separator)
	if !strings.HasPrefix(p.InstallDir, base) {
		return false
	}
	return os.RemoveAll(p.InstallDir) == nil
}
