package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangwanjie/gately/internal/config"
	"github.com/wangwanjie/gately/internal/logbuf"
	"github.com/wangwanjie/gately/internal/module"
)

func TestComparePluginVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.2.3", "1.2.3", 0, true},
		{"v1.2.4", "1.2.3", 1, true},
		{"1.10.0", "1.9.9", 1, true},
		{"1.2", "1.2.0", 0, true},
		{"1.0.0-rc1", "1.0.0", -1, true},
		{"1.0.0-rc2", "1.0.0-rc1", 1, true},
		{"1.0.0+build5", "1.0.0", 0, true},
		{"dev", "1.0.0", 0, false},
		{"1.0.x", "1.0.0", 0, false},
		{"", "1.0.0", 0, false},
	}
	for _, c := range cases {
		got, ok := comparePluginVersions(c.a, c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("comparePluginVersions(%q, %q) = (%d, %v)，want (%d, %v)", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

func TestManifestValidate(t *testing.T) {
	base := Manifest{Plugin: Plugin{ID: "hello", Name: "Hello", Kind: KindCommand}, Entry: "hello.py"}
	if err := base.validate(); err != nil {
		t.Fatalf("合法清单被拒：%v", err)
	}
	if len(base.Capabilities) != 1 || base.Capabilities[0] != "pages" {
		t.Errorf("空 capabilities 未默认补 pages：%v", base.Capabilities)
	}
	bad := []Manifest{
		{Plugin: Plugin{ID: "hello", Name: "Hello", Kind: KindCommand}},                                                // 缺 entry
		{Plugin: Plugin{ID: "hello", Name: "Hello", Kind: KindCommand}, Entry: "/abs/run"},                             // 绝对 entry
		{Plugin: Plugin{ID: "hello", Name: "Hello", Kind: KindURL}, Entry: "run"},                                      // URL 型无包
		{Plugin: Plugin{ID: "hello", Name: "Hello", Kind: KindCommand}, Entry: "run", Capabilities: []string{"hooks"}}, // 未来能力
		{Plugin: Plugin{ID: "Bad ID", Name: "Hello", Kind: KindCommand}, Entry: "run"},                                 // 非法 ID
	}
	for i, mf := range bad {
		if err := mf.validate(); err == nil {
			t.Errorf("第 %d 个非法清单未被拒", i)
		}
	}
}

func TestResolveEntry(t *testing.T) {
	if _, err := resolveEntry("/x/plugins/hello", "../evil"); err == nil {
		t.Error(".. 逃逸未被拒")
	}
	if _, err := resolveEntry("/x/plugins/hello", "/abs/run"); err == nil {
		t.Error("绝对路径未被拒")
	}
	p, err := resolveEntry("/x/plugins/hello", "bin/run.sh")
	if err != nil || p != "/x/plugins/hello/bin/run.sh" {
		t.Errorf("合法 entry 解析异常：%q, %v", p, err)
	}
}

// buildTestZip 生成一个含 plugin.json 与 entry 脚本的测试包。
func buildTestZip(t *testing.T, manifest, entry string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]struct {
		body string
		mode uint32
	}{
		"plugin.json": {manifest, 0o644},
		"run.sh":      {entry, 0o755},
	} {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(os.FileMode(content.mode))
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testModule(t *testing.T) *Module {
	t.Helper()
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.Init(module.Env{Store: store, Logs: logbuf.New(100, log.New(io.Discard, "", 0))}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStageZipGuards(t *testing.T) {
	m := testModule(t)
	// Zip-Slip
	var slip bytes.Buffer
	zw := zip.NewWriter(&slip)
	w, _ := zw.Create("../evil.txt")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	if _, err := m.stageZip(slip.Bytes(), "x"); err == nil {
		t.Error("Zip-Slip 路径未被拒")
	}
	// 缺 plugin.json
	var noManifest bytes.Buffer
	zw = zip.NewWriter(&noManifest)
	w, _ = zw.Create("run.sh")
	_, _ = w.Write([]byte("#!/bin/sh"))
	_ = zw.Close()
	if _, err := m.stageZip(noManifest.Bytes(), "x"); err == nil {
		t.Error("缺 plugin.json 的包未被拒")
	}
	// 符号链接
	var sym bytes.Buffer
	zw = zip.NewWriter(&sym)
	h := &zip.FileHeader{Name: "link", Method: zip.Deflate}
	h.SetMode(os.ModeSymlink | 0o777)
	w, _ = zw.CreateHeader(h)
	_, _ = w.Write([]byte("/etc/passwd"))
	_ = zw.Close()
	if _, err := m.stageZip(sym.Bytes(), "x"); err == nil {
		t.Error("符号链接条目未被拒")
	}
	// 合法包
	data := buildTestZip(t, `{"id":"hello","name":"Hello","kind":"command","entry":"run.sh"}`, "#!/bin/sh\nsleep 30\n")
	staging, err := m.stageZip(data, "hello")
	if err != nil {
		t.Fatalf("合法包解压失败：%v", err)
	}
	defer os.RemoveAll(staging)
	if _, err := os.Stat(filepath.Join(staging, "plugin.json")); err != nil {
		t.Error("staging 内缺少 plugin.json")
	}
}

func TestInstallAndUpgrade(t *testing.T) {
	m := testModule(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	manifestV1 := `{"id":"hello","name":"Hello","version":"0.1.0","kind":"command","entry":"run.sh","healthPath":"/health","options":{"greeting":"hi"}}`
	staging, err := m.stageZip(buildTestZip(t, manifestV1, "#!/bin/sh\nsleep 30\n"), "hello")
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	v, secret, err := m.installLocked(staging, "local", false)
	m.mu.Unlock()
	if err != nil {
		t.Fatalf("安装失败：%v", err)
	}
	if secret == "" {
		t.Error("新安装未返回密钥")
	}
	if v.Version != "0.1.0" || !v.Enabled || v.Source != "local" {
		t.Errorf("安装记录异常：%+v", v)
	}
	if !strings.HasPrefix(v.Command, v.InstallDir) || !strings.HasSuffix(v.Command, "run.sh") {
		t.Errorf("entry 未解析为安装目录下的绝对路径：%q", v.Command)
	}
	if v.Options["greeting"] != "hi" {
		t.Errorf("默认选项未写入：%v", v.Options)
	}
	if st, err := os.Stat(v.Command); err != nil || st.Mode()&0o111 == 0 {
		t.Errorf("entry 未置可执行位：%v", err)
	}
	// 旧进程已启动（sleep 30），升级时须先停、保留密钥、选项合并。
	m.mu.Lock()
	_, rt, _ := m.find("hello")
	runningBefore := rt.state == StateRunning
	m.mu.Unlock()
	if !runningBefore {
		t.Error("安装后插件未运行")
	}
	manifestV2 := `{"id":"hello","name":"Hello","version":"0.2.0","kind":"command","entry":"run.sh","healthPath":"/health","options":{"greeting":"hey","extra":true}}`
	staging2, err := m.stageZip(buildTestZip(t, manifestV2, "#!/bin/sh\nsleep 30\n"), "hello")
	if err != nil {
		t.Fatal(err)
	}
	// 升级前先改一个选项，验证升级保留管理员的值、只补新增默认值。
	m.mu.Lock()
	p, _, _ := m.find("hello")
	p.Options["greeting"] = "custom"
	m.mu.Unlock()
	m.mu.Lock()
	v2, secret2, err := m.installLocked(staging2, "registry:src1", true)
	m.mu.Unlock()
	if err != nil {
		t.Fatalf("升级失败：%v", err)
	}
	if v2.Version != "0.2.0" {
		t.Errorf("升级后版本未变：%q", v2.Version)
	}
	if secret2 != secret {
		t.Error("升级更换了密钥（应保留）")
	}
	if v2.Options["greeting"] != "custom" {
		t.Errorf("升级覆盖了管理员改过的选项：%v", v2.Options)
	}
	if v2.Options["extra"] != true {
		t.Errorf("升级未补新清单的默认值：%v", v2.Options)
	}
	if v2.Source != "registry:src1" {
		t.Errorf("升级后来源未更新：%q", v2.Source)
	}
}

func TestSourcesPlaceholderAndLocalIndex(t *testing.T) {
	m := testModule(t)
	m.mu.Lock()
	srcs := m.sourcesLocked()
	m.mu.Unlock()
	if len(srcs) != 1 || srcs[0].ID != "official" || srcs[0].Kind != "official" || srcs[0].URL != "" {
		t.Fatalf("官方空源占位异常：%+v", srcs)
	}
	if _, err := m.fetchIndex(srcs[0]); err == nil {
		t.Error("空官方源取索引应报错")
	}
	// 本地目录源：index.json + 相对路径安装包
	dir := t.TempDir()
	index := `{"name":"test","plugins":[{"id":"hello","name":"Hello","version":"0.1.0","kind":"command","entry":"run.sh","downloadUrl":"hello.zip"}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := buildTestZip(t, `{"id":"hello","name":"Hello","version":"0.1.0","kind":"command","entry":"run.sh"}`, "#!/bin/sh\nsleep 1\n")
	if err := os.WriteFile(filepath.Join(dir, "hello.zip"), pkg, 0o644); err != nil {
		t.Fatal(err)
	}
	src := &RegistrySource{ID: "local1", Name: "本地", Kind: "community", Dir: dir}
	idx, err := m.fetchIndex(src)
	if err != nil {
		t.Fatalf("本地索引读取失败：%v", err)
	}
	if len(idx.Plugins) != 1 || idx.Plugins[0].DownloadURL != "hello.zip" {
		t.Fatalf("索引解析异常：%+v", idx)
	}
	data, err := m.downloadPackage(src, "hello.zip")
	if err != nil {
		t.Fatalf("相对路径安装包读取失败：%v", err)
	}
	if _, err := m.stageZip(data, "hello"); err != nil {
		t.Fatalf("本地源安装包解压失败：%v", err)
	}
	if _, err := m.downloadPackage(src, "../escape.zip"); err == nil {
		t.Error("逃出源目录的 downloadUrl 未被拒")
	}
}

func TestRegistrySourceValidate(t *testing.T) {
	valid := RegistrySource{ID: "s1", Name: "社区", Kind: "community", URL: "https://example.com/index.json"}
	if err := valid.validate(); err != nil {
		t.Errorf("合法来源被拒：%v", err)
	}
	bad := []RegistrySource{
		{ID: "s1", Name: "x", Kind: "community"},                                        // URL 与目录均空
		{ID: "s1", Name: "x", Kind: "community", URL: "https://a/", Dir: "/tmp"},        // 两者都填
		{ID: "s1", Name: "x", Kind: "community", URL: "ftp://a/index.json"},             // 非 http(s)
		{ID: "s1", Name: "x", Kind: "weird", URL: "https://a/index.json"},               // 非法类型
		{ID: "s1", Name: "x", Kind: "community", Dir: "/definitely/not/a/real/dir/xyz"}, // 不存在的目录
	}
	for i, s := range bad {
		if err := s.validate(); err == nil {
			t.Errorf("第 %d 个非法来源未被拒", i)
		}
	}
}
