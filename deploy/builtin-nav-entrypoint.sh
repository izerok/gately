#!/bin/bash
# Gately + 内置导航页：容器入口脚本。
#
# 首次启动流程：启动 Gately → 用默认管理员登录 → 查插件列表 →
# 若导航页未装，走插件模块自身的本地目录安装接口
# （POST /api/plugin/install/local）把预置的 /opt/gately/builtin-plugins/nav
# 装好并启用 → 轮询到 running。已装过则跳过。
#
# 不烘焙任何密钥：登录用 Gately 自身的默认管理员（admin / gately.cn，
# 可用 GATELY_INITIAL_PASSWORD 覆盖）；首登改密码横幅由 Gately 自己提示。
# 安装环节任何失败只记日志，不阻塞 Gately 主进程。
set -u

GATELY_BIN=/usr/local/bin/gately
BUILTIN_DIR=/opt/gately/builtin-plugins/nav
PLUGIN_ID=nav
CONFIG_DIR=${GATELY_CONFIG_DIR:-/etc/gately}
LISTEN_ADDR=${GATELY_LISTEN:-:16680}
ADMIN_USER=${GATELY_ADMIN_USER:-admin}
ADMIN_PASS=${GATELY_INITIAL_PASSWORD:-gately.cn}
BASE_URL=http://127.0.0.1:16680

mkdir -p "$CONFIG_DIR"

"$GATELY_BIN" -c "$CONFIG_DIR" -listen "$LISTEN_ADDR" &
GATELY_PID=$!
trap 'kill "$GATELY_PID" 2>/dev/null; wait "$GATELY_PID" 2>/dev/null' TERM INT

# 等管理界面可达（拿到任意 HTTP 响应即算就绪）
for _ in $(seq 1 60); do
  if curl -s -o /dev/null "$BASE_URL/" 2>/dev/null; then
    break
  fi
  sleep 1
done

# 内置安装（尽力而为；失败只告警，Gately 照常跑）
BUILTIN_DIR="$BUILTIN_DIR" PLUGIN_ID="$PLUGIN_ID" BASE_URL="$BASE_URL" \
  ADMIN_USER="$ADMIN_USER" ADMIN_PASS="$ADMIN_PASS" python3 - <<'PYEOF' || echo "[builtin] setup failed, gately keeps running"
import json
import os
import sys
import time
import urllib.error
import urllib.request
import http.cookiejar

base = os.environ["BASE_URL"]
plugin_id = os.environ["PLUGIN_ID"]
opener = urllib.request.build_opener(
    urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def call(method, path, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(
        base + path, data=data, method=method,
        headers={"Content-Type": "application/json"})
    try:
        with opener.open(req, timeout=10) as resp:
            return resp.status, json.loads(resp.read().decode() or "null")
    except urllib.error.HTTPError as e:
        try:
            body = e.read().decode()
        except Exception:
            body = ""
        return e.code, body
    except Exception as e:
        return 0, str(e)


def log(msg):
    print(f"[builtin] {msg}", flush=True)


code, _ = call("POST", "/api/login", {
    "username": os.environ["ADMIN_USER"],
    "password": os.environ["ADMIN_PASS"],
})
if code != 200:
    log(f"login failed (HTTP {code}); skip builtin install "
        "(password may have been changed)")
    sys.exit(0)

code, plugins = call("GET", "/api/plugin/plugins")
if code != 200 or not isinstance(plugins, list):
    log(f"list plugins failed (HTTP {code}); skip")
    sys.exit(0)

if not any(p.get("id") == plugin_id for p in plugins):
    code, resp = call("POST", "/api/plugin/install/local",
                      {"dir": os.environ["BUILTIN_DIR"]})
    if code != 200:
        log(f"install failed (HTTP {code}): {resp}")
        sys.exit(0)
    log("nav installed via local-dir install")
else:
    log("nav already installed, skip install")


def poll_running(rounds):
    for _ in range(rounds):
        code, st = call("GET", f"/api/plugin/plugins/{plugin_id}/status")
        if code == 200 and isinstance(st, dict) and st.get("state") == "running":
            log(f"nav running (port {st.get('port')})")
            return True
        time.sleep(1)
    return False


if not poll_running(30):
    code, _ = call("POST", f"/api/plugin/plugins/{plugin_id}/start")
    log(f"nav not running after wait, start attempted (HTTP {code})")
    if not poll_running(30):
        log("nav still not running; check plugin logs in admin UI")
PYEOF

wait "$GATELY_PID"
