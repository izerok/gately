#!/usr/bin/env python3
"""最小 Gately 插件示例（纯标准库）。

监听 Gately 分配的 PLUGIN_PORT：
  GET /        回显反代注入的 X-Gately-* 头（证明请求经 Gately 而来）
  GET /health  健康检查端点，返回 ok

注册方式（见 README.md）：类型选 command，可执行文件填本文件绝对路径，
端口填 0（自动分配），健康检查填 /health。
"""
import html
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def _send(self, body: bytes, ctype: str):
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            self._send(b"ok", "text/plain")
            return
        rows = "".join(
            f"<tr><td>{html.escape(k)}</td><td>{html.escape(v)}</td></tr>"
            for k, v in sorted(self.headers.items())
            if k.lower().startswith("x-gately-")
        )
        page = f"""<!doctype html><html><head><meta charset="utf-8">
<title>Hello Plugin</title></head><body>
<h1>Hello from a Gately plugin</h1>
<p>以下是由 Gately 反代注入的身份头（浏览器原始 Cookie 已被剥离）：</p>
<table border="1" cellpadding="6">{rows or "<tr><td colspan=2>（未收到 X-Gately-* 头：请确认经 Gately 反代访问）</td></tr>"}</table>
</body></html>"""
        self._send(page.encode(), "text/html; charset=utf-8")

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    port = int(os.environ.get("PLUGIN_PORT", "0")) or 18080
    print(f"hello plugin listening on 127.0.0.1:{port} (id={os.environ.get('GATELY_PLUGIN_ID')})", flush=True)
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
