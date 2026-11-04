#!/usr/bin/env python3
"""Gately 命令型插件：导航页（纯标准库）。

静态链接按分组展示，页内搜索做客户端过滤；v1 不探测目标站点存活。
链接管理在插件自己的页面里完成，数据存 links.json。

监听 Gately 分配的 PLUGIN_PORT：
  GET  /           导航页（分组链接 + 搜索框；admin 另见管理入口）
  GET  /health     健康检查端点，返回 ok
  GET  /api/links  当前链接文档（JSON）
  POST /api/links  整体替换链接文档（仅 X-Gately-Role: admin，见下）

环境变量：
  PLUGIN_PORT     Gately 分配的监听端口（直跑时缺省 18081）
  PLUGIN_OPTIONS  JSON，可含 title（页标题，缺省“导航页”）、
                  dataFile（links.json 的存放路径，缺省为本文件同目录）

权限模型：页面经 Gately 反代访问时，反代注入 X-Gately-Role 头；
写接口只认 admin，readonly 与无头（直接访问端口）一律 403。
页面内的编辑入口也只在 role=admin 时渲染，服务端校验为准。
"""
import html
import json
import os
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MAX_BODY = 1024 * 1024
MAX_GROUPS = 50
MAX_LINKS_PER_GROUP = 200

_script_dir = os.path.dirname(os.path.abspath(__file__))


def _options():
    try:
        opts = json.loads(os.environ.get("PLUGIN_OPTIONS", "") or "{}")
    except ValueError:
        opts = {}
    return opts if isinstance(opts, dict) else {}


_OPTIONS = _options()
TITLE = _OPTIONS.get("title") or "导航页"
_DATA_FILE = _OPTIONS.get("dataFile") or os.path.join(_script_dir, "links.json")
if isinstance(_DATA_FILE, str):
    _DATA_FILE = os.path.expanduser(_DATA_FILE)

_lock = threading.Lock()


def _clean_str(value, max_len):
    return isinstance(value, str) and 0 < len(value) <= max_len


def validate_doc(doc):
    """校验链接文档，返回清洗后的文档；不合法则返回 None。"""
    if not isinstance(doc, dict):
        return None
    groups = doc.get("groups")
    if not isinstance(groups, list) or len(groups) > MAX_GROUPS:
        return None
    clean_groups = []
    for group in groups:
        if not isinstance(group, dict):
            return None
        name = group.get("name")
        links = group.get("links")
        if not _clean_str(name, 50) or not isinstance(links, list):
            return None
        if len(links) > MAX_LINKS_PER_GROUP:
            return None
        clean_links = []
        for link in links:
            if not isinstance(link, dict):
                return None
            title = link.get("title")
            url = link.get("url")
            desc = link.get("desc", "")
            if not _clean_str(title, 100) or not _clean_str(url, 2000):
                return None
            if not (url.startswith("http://") or url.startswith("https://")):
                return None
            if not isinstance(desc, str) or len(desc) > 500:
                return None
            clean_links.append({"title": title, "url": url, "desc": desc})
        clean_groups.append({"name": name, "links": clean_links})
    return {"groups": clean_groups}


def load_doc():
    try:
        with open(_DATA_FILE, "r", encoding="utf-8") as f:
            doc = validate_doc(json.load(f))
    except (OSError, ValueError):
        doc = None
    return doc if doc is not None else {"groups": []}


def save_doc(doc):
    cleaned = validate_doc(doc)
    if cleaned is None:
        return None
    data = json.dumps(cleaned, ensure_ascii=False, indent=2).encode("utf-8")
    directory = os.path.dirname(_DATA_FILE) or "."
    fd, tmp = tempfile.mkstemp(dir=directory, prefix=".links.", suffix=".tmp")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, _DATA_FILE)
    except OSError:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    return cleaned


PAGE = """<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>__TITLE__</title>
<style>
body{font-family:sans-serif;max-width:900px;margin:0 auto;padding:16px;color:#222}
h1{font-size:22px}h2{font-size:17px;border-bottom:1px solid #ddd;padding-bottom:4px}
#search{width:100%;padding:8px;font-size:15px;box-sizing:border-box}
ul{list-style:none;padding:0}li{padding:6px 0;border-bottom:1px solid #f0f0f0}
li a{font-size:15px}li .desc{color:#666;font-size:13px;margin-left:8px}
button{margin:2px;padding:4px 10px;cursor:pointer}
input,select{padding:5px;margin:2px}fieldset{margin:14px 0}
#msg{color:#1a7f64;min-height:20px}.err{color:#b00}
.admin-only{display:none}body[data-role="admin"] .admin-only{display:block}
body[data-role="admin"] .admin-inline{display:inline-block}
.admin-inline{display:none}
</style></head>
<body data-role="__ROLE__">
<h1>__TITLE__</h1>
<input id="search" type="search" placeholder="搜索链接（按标题、地址、说明过滤）">
<div id="groups"></div>
<p id="nomatch" style="display:none;color:#666">没有匹配的链接。</p>
<fieldset class="admin-only"><legend>新增分组</legend>
<input id="add-group-name" placeholder="分组名">
<button id="add-group-btn">新增分组</button></fieldset>
<fieldset class="admin-only"><legend>新增链接</legend>
<select id="add-group"></select>
<input id="add-title" placeholder="标题">
<input id="add-url" placeholder="https://…" size="40">
<input id="add-desc" placeholder="说明（可选）">
<button id="add-link-btn">新增链接</button></fieldset>
<p><button id="export-btn">导出 JSON</button>
<span class="admin-only"><input type="file" id="import-file" accept=".json,application/json">
<button id="import-btn">导入 JSON</button></span></p>
<p id="msg"></p>
<script>
let doc = {groups: []};
const esc = s => s.replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
const isAdmin = document.body.dataset.role === "admin";
const msgEl = document.getElementById("msg");
function showMsg(text, isErr) { msgEl.textContent = text; msgEl.className = isErr ? "err" : ""; }
async function load() {
  const r = await fetch("api/links");
  doc = await r.json();
  render();
}
async function save() {
  const r = await fetch("api/links", {method: "POST",
    headers: {"Content-Type": "application/json"}, body: JSON.stringify(doc)});
  if (!r.ok) { const e = await r.json().catch(() => ({})); showMsg("保存失败：" + (e.error || r.status), true); return false; }
  doc = await r.json();
  showMsg("已保存。");
  return true;
}
function render() {
  const q = document.getElementById("search").value.trim().toLowerCase();
  const wrap = document.getElementById("groups");
  wrap.innerHTML = "";
  let shown = 0;
  doc.groups.forEach((g, gi) => {
    const links = g.links.filter(l =>
      !q || l.title.toLowerCase().includes(q) || l.url.toLowerCase().includes(q) ||
      (l.desc || "").toLowerCase().includes(q));
    if (!links.length) return;
    shown += links.length;
    const sec = document.createElement("section");
    let h = "<h2>" + esc(g.name) + "</h2>";
    if (isAdmin) h += ' <button data-del-group="' + gi + '">删除分组</button>';
    sec.innerHTML = h;
    const ul = document.createElement("ul");
    links.forEach(l => {
      const idx = g.links.indexOf(l);
      const li = document.createElement("li");
      li.innerHTML = '<a href="' + esc(l.url) + '" target="_blank" rel="noopener">' + esc(l.title) + "</a>" +
        (l.desc ? '<span class="desc">' + esc(l.desc) + "</span>" : "") +
        (isAdmin ? ' <button class="admin-inline" data-edit="' + gi + ":" + idx + '">编辑</button>' +
                   ' <button class="admin-inline" data-del="' + gi + ":" + idx + '">删除</button>' : "");
      ul.appendChild(li);
    });
    sec.appendChild(ul);
    wrap.appendChild(sec);
  });
  document.getElementById("nomatch").style.display = shown ? "none" : "block";
  const sel = document.getElementById("add-group");
  if (sel) sel.innerHTML = doc.groups.map((g, i) => '<option value="' + i + '">' + esc(g.name) + "</option>").join("");
}
document.getElementById("search").addEventListener("input", render);
document.getElementById("groups").addEventListener("click", async e => {
  const t = e.target;
  if (t.dataset.delGroup !== undefined) {
    const gi = +t.dataset.delGroup;
    if (doc.groups[gi].links.length && !confirm("分组内还有链接，确认删除整个分组？")) return;
    doc.groups.splice(gi, 1);
    if (await save()) render();
  } else if (t.dataset.del !== undefined) {
    const [gi, li] = t.dataset.del.split(":").map(Number);
    if (!confirm("确认删除该链接？")) return;
    doc.groups[gi].links.splice(li, 1);
    if (await save()) render();
  } else if (t.dataset.edit !== undefined) {
    const [gi, li] = t.dataset.edit.split(":").map(Number);
    const link = doc.groups[gi].links[li];
    const row = t.closest("li");
    row.innerHTML = '<input class="e-title" value="' + esc(link.title) + '"> ' +
      '<input class="e-url" value="' + esc(link.url) + '" size="40"> ' +
      '<input class="e-desc" value="' + esc(link.desc || "") + '"> ' +
      '<button class="e-save">保存</button> <button class="e-cancel">取消</button>';
    row.querySelector(".e-save").onclick = async () => {
      const nt = row.querySelector(".e-title").value.trim();
      const nu = row.querySelector(".e-url").value.trim();
      if (!nt || !(nu.startsWith("http://") || nu.startsWith("https://"))) { showMsg("标题不能为空，地址须以 http(s):// 开头。", true); return; }
      doc.groups[gi].links[li] = {title: nt, url: nu, desc: row.querySelector(".e-desc").value.trim()};
      if (await save()) render();
    };
    row.querySelector(".e-cancel").onclick = render;
  }
});
const addGroupBtn = document.getElementById("add-group-btn");
if (addGroupBtn) addGroupBtn.onclick = async () => {
  const name = document.getElementById("add-group-name").value.trim();
  if (!name) { showMsg("分组名不能为空。", true); return; }
  doc.groups.push({name: name, links: []});
  document.getElementById("add-group-name").value = "";
  if (await save()) render();
};
const addLinkBtn = document.getElementById("add-link-btn");
if (addLinkBtn) addLinkBtn.onclick = async () => {
  const gi = +document.getElementById("add-group").value;
  const title = document.getElementById("add-title").value.trim();
  const url = document.getElementById("add-url").value.trim();
  const desc = document.getElementById("add-desc").value.trim();
  if (!title || !(url.startsWith("http://") || url.startsWith("https://"))) { showMsg("标题不能为空，地址须以 http(s):// 开头。", true); return; }
  doc.groups[gi].links.push({title: title, url: url, desc: desc});
  document.getElementById("add-title").value = "";
  document.getElementById("add-url").value = "";
  document.getElementById("add-desc").value = "";
  if (await save()) render();
};
document.getElementById("export-btn").onclick = async () => {
  const r = await fetch("api/links");
  const blob = new Blob([await r.text()], {type: "application/json"});
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob); a.download = "links.json"; a.click();
  URL.revokeObjectURL(a.href);
};
const importBtn = document.getElementById("import-btn");
if (importBtn) importBtn.onclick = async () => {
  const f = document.getElementById("import-file").files[0];
  if (!f) { showMsg("请先选择要导入的 JSON 文件。", true); return; }
  let data;
  try { data = JSON.parse(await f.text()); } catch (e) { showMsg("导入失败：不是合法 JSON。", true); return; }
  if (!data || !Array.isArray(data.groups)) { showMsg("导入失败：文件里没有 groups 数组。", true); return; }
  doc = data;
  if (await save()) render();
};
load();
</script></body></html>"""


class Handler(BaseHTTPRequestHandler):
    def _send(self, body: bytes, ctype: str, status: int = 200):
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_json(self, obj, status: int = 200):
        self._send(json.dumps(obj, ensure_ascii=False).encode("utf-8"),
                   "application/json; charset=utf-8", status)

    def _role(self):
        return self.headers.get("X-Gately-Role") or "readonly"

    def do_GET(self):
        if self.path == "/health":
            self._send(b"ok", "text/plain")
            return
        if self.path in ("/", ""):
            page = PAGE.replace("__TITLE__", html.escape(TITLE)).replace(
                "__ROLE__", html.escape(self._role()))
            self._send(page.encode("utf-8"), "text/html; charset=utf-8")
            return
        if self.path == "/api/links":
            with _lock:
                self._send_json(load_doc())
            return
        self._send(b"not found", "text/plain", 404)

    def do_POST(self):
        if self.path != "/api/links":
            self._send(b"not found", "text/plain", 404)
            return
        if self._role() != "admin":
            self._send_json({"error": "readonly role cannot write"}, 403)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = 0
        if length <= 0 or length > MAX_BODY:
            self._send_json({"error": "body too large or missing"}, 400)
            return
        try:
            doc = json.loads(self.rfile.read(length).decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            self._send_json({"error": "invalid JSON"}, 400)
            return
        with _lock:
            try:
                saved = save_doc(doc)
            except OSError:
                self._send_json({"error": "failed to write links file"}, 500)
                return
        if saved is None:
            self._send_json({"error": "invalid links document"}, 400)
            return
        self._send_json(saved)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    port = int(os.environ.get("PLUGIN_PORT", "0")) or 18081
    print(f"nav plugin listening on 127.0.0.1:{port} "
          f"(id={os.environ.get('GATELY_PLUGIN_ID')}, data={_DATA_FILE})", flush=True)
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
