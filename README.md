# Dev Console · 通用设备服务平台

一个 Go 可执行文件提供 Vue 中文 Web、项目服务 API、SQLite 和串口设备接入。支持 Linux/amd64，首批设备为龙威 LW-3010E 与 ITECH IT6722；具体协议位于 `internal/access/`，平台通过统一设备服务契约组织调用。

支持通过社区协议扫码连接中国大陆米家账号，预览并勾选导入开关设备，见[米家开关接入](docs/usage/operations/xiaomi.mdoc)。该接入已完成隔离模拟验证，并据公开型号规格补齐五路插座通道；实际开关动作仍须按明确授权的设备范围验收。

目标机安装、管理员初始化和第一次项目调用见[快速开始](docs/usage/start/index.mdoc)。开发者在仓库根执行：

```sh
npm run build
./dist/platform-linux-amd64 admin bootstrap --data-dir ./data --username admin
./dist/platform-linux-amd64 serve
```

浏览器访问 `http://<服务器实际IP>:8080`，使用刚初始化的账号与密码；没有默认通用密码。工具链、配置与源码启动见[开发环境](docs/development/start.mdoc)。目标机发布安装及 systemd 管理见[安装说明](docs/usage/operations/installation.mdoc)。

设备、项目和 Token 的操作见[使用文档](docs/usage/index.mdoc)；实现、测试与发布见[开发文档](docs/development/index.mdoc)。

## 文档

正式入口为 [docs/index.mdoc](docs/index.mdoc)。在仓库根使用已全局安装的 edoc：

```sh
npm run docs:check
npm run docs:build
```

打开生成的 `output/index.html` 可离线阅读。正文按使用和开发分区，各层入口安排阅读顺序；维护方法见[文档维护](docs/development/documentation.mdoc)。生成文件和缓存不提交。
