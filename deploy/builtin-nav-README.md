# Gately + 内置导航页（演示镜像）

在上游 `Dockerfile` 的多阶段路子上加的变体：镜像里预置导航页插件
（`examples/plugin-nav/`），容器首次启动时入口脚本自动把它装好并启用，
开箱即有，不用手动安装。上游 `Dockerfile` 本身不动，官方镜像不受影响。

## 首次启动流程（一句话）

入口脚本（`deploy/builtin-nav-entrypoint.sh`）启动 Gately 后，用默认管理员
登录，走插件模块自身的本地目录安装接口
（`POST /api/plugin/install/local`，目录 `/opt/gately/builtin-plugins/nav`）
把导航页装好并启用，轮询到 running；已装过则跳过，安装失败不阻塞主进程。

## 构建

沙箱里构建（出口是 TLS 拦截代理，需把宿主 CA 挂进构建容器；
`--network=host --dns` 是沙箱专用：构建容器的 DNS 默认不通；
`--layers=false` 也是沙箱专用：沙箱用 vfs 驱动，每层都是全量拷贝，
home 配额吃紧，关掉中间层只留每阶段一层才装得下）：

```sh
podman build --layers=false --network=host --dns 198.19.0.1 -f Dockerfile.builtin-nav \
  -v /usr/local/share/ca-certificates/hatch-egress-ca.crt:/tmp/hatch-egress-ca.crt \
  -t gately-nav-builtin:local .
```

自己机器上构建（去掉 `-v` 那行即可；`docker` 与 `podman` 命令互通）：

```sh
podman build -f Dockerfile.builtin-nav -t gately-nav-builtin:local .
```

## 运行

```sh
podman run -d --name gately-nav -p 16680:16680 \
  -v gately-nav-data:/etc/gately gately-nav-builtin:local
```

然后打开 `http://<主机>:16680`，默认管理员 `admin / gately.cn` 登录
（首登横幅会提示改密码，请改掉）；工具 → 插件中心里导航页已是 running，
点“打开”即见导航页。

## 说明

- 镜像没有发布到任何 registry；上面命令是在本机从源码构建。
- 数据在 `/etc/gately`（挂载卷里）：导航页的链接数据在其中
  `plugins/nav/links.json`。导航页升级是重新安装，会盖掉安装目录，
  升级前先在导航页点“导出 JSON”备份，升级后再“导入 JSON”恢复；
  也可把插件选项 `dataFile` 指到安装目录之外以避开这个问题。
- 若启动时已改过管理员密码（数据卷复用），入口脚本登录失败会跳过
  内置安装并记一行日志，Gately 照常跑；此时手动装一次即可。
  如需用环境变量指定初始密码，设 `GATELY_INITIAL_PASSWORD`，
  入口脚本登录时会用它。
- 与上游运行阶段的区别：本镜像不带 docker-cli / nftables
  （演示范围用不到），带了 `python3`（导航页是纯标准库 Python 服务）
  与 `curl`/`bash`（入口脚本调用安装接口用）。
