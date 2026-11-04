# 示例插件：hello

一个最小的命令型插件，证明插件协议 v1 跑得通（见 `docs/plugin-protocol-v1.md`）。

## 运行

```sh
chmod +x hello.py
# 本地直跑（不用 Gately 时）：PLUGIN_PORT=18080 ./hello.py
# 然后 curl http://127.0.0.1:18080/ 与 /health
```

## 在 Gately 里注册

1. 把 `hello.py` 放到 Gately 所在机器，例如 `/usr/local/bin/plugin-hello.py`；
2. 管理界面 → 插件中心 → 添加插件：
   - ID：`hello`；名称：`Hello`；
   - 类型：命令；可执行文件：`/usr/local/bin/plugin-hello.py`；
   - 端口：`0`（自动分配）；健康检查：`/health`；启用：开；
3. 点“打开”，应看到回显页，其中 `X-Gately-User` 为当前登录用户，
   `X-Gately-Plugin-Token` 为创建时返回的密钥（示例仅回显、不校验；
   生产插件应对照创建时保存的密钥校验该头）。
