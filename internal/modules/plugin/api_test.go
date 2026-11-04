package plugin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestPageProxyStripsFrameOptions：插件页由管理后台同源 iframe 展示，
// 反代响应不得带 X-Frame-Options——全局中间件预置的与上游自带的都要去掉，
// 其他安全头（如 nosniff）保持原样。
func TestPageProxyStripsFrameOptions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟插件服务自带帧头（默认 DENY 的框架很常见）
		w.Header().Set("X-Frame-Options", "DENY")
		w.Write([]byte("hello plugin"))
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	plug := &Plugin{ID: "hello", Kind: KindURL, Enabled: true}
	m := New()
	m.cfg.Plugins = []*Plugin{plug}
	m.rts["hello"] = &runtime{plug: plug, state: StateRunning, target: target}

	req := httptest.NewRequest(http.MethodGet, "/api/plugin/pages/hello/", nil)
	req.SetPathValue("id", "hello")
	rec := httptest.NewRecorder()
	// 模拟全局安全中间件在 handler 之前预置的头
	rec.Header().Set("X-Frame-Options", "DENY")
	rec.Header().Set("X-Content-Type-Options", "nosniff")
	m.handlePageProxy(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q, want empty", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "hello plugin" {
		t.Fatalf("body = %q, want hello plugin", body)
	}
}
