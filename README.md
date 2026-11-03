# 通用设备服务平台

用一个 Go 可执行文件提供 Vue 中文管理页面、项目服务 API、SQLite 持久化及串口设备接入。支持桌面和手机布局，默认允许局域网访问。

首批设备：龙威 LW-3010E（Modbus RTU）和 ITECH IT6722（SCPI）。平台核心只认识设备及服务契约，具体协议位于 `internal/access/`；设备控制、页面调试与 Agent 调用复用同一执行链。

## 构建与启动

实际验证环境：Linux/amd64，Go **1.26.3**、Node.js **24.15.0**、npm **11.12.1**。构建需要现有 Go、Node、npm 及依赖源网络访问；脚本不会安装全局工具。依赖版本已锁定。

```sh
# 在本仓库code目录执行；自动npm ci、类型检查、前端构建、Go依赖下载和CGO_ENABLED=0编译
npm run build -- --target linux/amd64

# 首次初始化，交互输入密码；至少10个字符、不超过72字节
./dist/platform-linux-amd64 admin bootstrap --data-dir ./data --username admin

# 使用默认配置，监听0.0.0.0:8080
./dist/platform-linux-amd64 serve

# 或显式使用配置文件
./dist/platform-linux-amd64 serve --config ./configs/config.example.yaml
```

浏览器访问 `http://<服务器局域网IP>:8080`，使用刚初始化的账号和密码登录。没有固定默认密码，也没有远程开放注册入口。程序日志输出实际访问地址。`0.0.0.0` 是监听地址，不能作为客户端访问地址。

管理员依次创建设备、创建项目、添加项目服务并绑定设备能力，再创建项目 Token。将项目文档 URL 与单独保存的 Token 交给 Agent 即可调用。Token 原文只展示一次。

运行无需 Node/npm、Go、Python、外部数据库服务器或 CDN。数据、日志配置不嵌入可执行文件。`--password-stdin` 仅供明确指定的自动化初始化，从标准输入读取密码，不把密码放入命令行。

## 配置与设备

配置字段见 [配置样例](configs/config.example.yaml)：`listen`、`public_base_url`、`data_dir`。相对数据目录相对于进程工作目录。

- 默认监听 `0.0.0.0:8080`；仅本机访问可改为 `127.0.0.1:8080`。
- `public_base_url` 留空时选择首个非回环 IPv4；多网卡和反向代理部署应明确填写实际地址。文档链接不会使用客户端传入的 Host 或转发头。
- 直接局域网 HTTP 可运行；使用 HTTPS 代理时填写 HTTPS 外部地址，Session Cookie 随之启用 Secure。代理需保持页面与 API 同源。
- 数据目录包含 `platform.db` 及运行中的 WAL 文件，目录权限0700、数据库0600。一个数据目录只运行一个平台进程。

串口通过网页配置，不写死在驱动。2026-11-02的IT6722、2026-11-03的龙威分别在当次接入的 `/dev/ttyUSB0` 完成验收；端口编号可能随插拔变化，不能据此认定设备身份。默认9600、8N1、无流控，龙威本次从机地址为1。可设置更保守的设备电压和电流边界，项目服务只能在设备边界内进一步收紧。设定分辨率为0.01V/0.01A，拒绝越界和无法表示的输入。

查看设备节点实际权限后给服务用户授权，例如先运行 `ls -l /dev/ttyUSB0` 与 `getfacl /dev/ttyUSB0`。组名以该机器实际设备节点为准；不要用 root 运行整个 Web 服务解决串口权限。

页面的“配置 → 可用串口”支持ttyUSB和ttyACM候选，区分唯一USB序列号by-id、固定USB插口by-path及手动路径。稳定路径原样保存，每次连接重新解析实际节点；实际节点只用作连接共享和busy键。无序列号或身份重复的by-id会拒绝使用，当前龙威的CH340应选择固定插口by-path。

**更换串口无需删除设备**：编辑原设备，重新选择或填写路径并保存，再点“连接测试”。设备ID、项目服务绑定、对外URL和原Token保留；IT6722原有的仪器序列号校验继续生效，通信忙碌时拒绝更改。旧配置不会自动猜测迁移，原先保存ttyUSB0的设备需要手动选择一次稳定路径。细节见 [稳定绑定说明](docs/serial-binding.md)。

同串口的多个 Modbus 地址共享一个连接。相同路径与从机地址重复、共享总线参数不一致、独占串口复用都会被拒绝。离线设备仍可保存身份与项目绑定，重新连接不会改变设备输出。IT6722首次成功查询后记住设备序列号，重连时拒绝同型号但身份不同的设备；替换实物应显式登记新设备并改绑项目。

## 调用与执行语义

每个项目提供版本化 Markdown 与 OpenAPI3.1：

```text
GET  /api/v1/projects/<projectKey>/docs
GET  /api/v1/projects/<projectKey>/openapi.json
POST /api/v1/projects/<projectKey>/services/<serviceKey>/invoke
```

```sh
curl -X POST 'http://<服务器IP>:8080/api/v1/projects/board-test-a/services/board_power/invoke' \
  -H 'Authorization: Bearer <ACCESS_TOKEN>' \
  -H 'Content-Type: application/json' \
  --data '{"parameters":{"enabled":true}}'
```

设备服务包括 `output.set`、`voltage.set`、`current_limit.set`、`settings.read`、`measurement.read`、`output.read`。项目服务可取业务名称，不依赖底层型号。普通用户及 Token 只能调用项目服务；管理员还能在设备详情直接调试已注册服务。没有原始串口、SCPI、HEX 或 Shell 透传接口。

通信资源采用非阻塞 busy：正在通信就返回409 `RESOURCE_BUSY`，不排队、不自动重试。“设置并回读”不可穿插其他请求。设置成功要求 `verified`，指设定值或输出状态回读一致，不代表外部测量精度或输出稳定时间已验收。`observed_at` 为 UTC，`source=live` 表示实时采集。

超时可能返回504 `OUTCOME_UNKNOWN`：设备可能已执行，不能当成动作未发生。旧通信关闭及有界回复隔离后才能开始下一次通信；失败不触发隐式反向操作。连接、启动、重启和退出都不会自动开输出或恢复历史设定。

配置修改使用 revision 防止覆盖其他编辑；服务绑定保存契约快照，兼容换设备可保持调用URL。设备范围收紧或服务失踪会使旧绑定失效，需要管理员明确处理。删除服务和项目后不复用原 key，避免旧调用方意外操作新服务。

## 测试

```sh
# 首先构建前端，供Go embed与程序验收使用
npm run build -- --target linux/amd64

# 协议、Schema、真实HTTP、持久化、权限与生命周期
npm test
npm run test:race

# 已有playwright-cli + 指定目录Chromium；不会下载浏览器
npm run test:browser

# 最终单程序无工具链运行、停机备份恢复，以及测试工具中断恢复的模拟验证
npm run test:runtime
npm run test:hardware-cancel
npm run test:hardware-cancel -- longwei-lw3010e
```

浏览器验收使用 `~/work/software/playwright-browsers/chromium-1237/chrome-linux64/chrome`，可通过 `PLAYWRIGHT_CHROMIUM_EXECUTABLE` 指向该目录内其他已有 Chromium。测试在本项目临时配置中设置 `chromiumSandbox:false` 以适配本机沙箱限制，不改变系统配置。验证最终 Go 程序嵌入的页面、真实 API 及独立伪终端模拟器；依次覆盖1440×900、390×844和360×800，手机配置开启触摸模拟。

测试创建一次性管理员、数据目录、模拟串口和浏览器会话，完成后清理。使用18480端口，运行前需确认未被其他服务占用。截图与脱敏结果写入 `docs/evidence/`。手机视口与触摸模拟不等于实体手机验收；本机浏览器访问局域网地址不等于跨终端网络验证。

`cmd/simulator` 和 `internal/testdevice` 是测试工具。模拟器不引用生产驱动的帧/CRC实现，历史真实帧作为外部夹具。Go测试覆盖共享总线busy、取消清理期间不释放busy、失败不重试、审计写入失败不发送控制命令、权限隔离及重启删除等行为。

### 两款电源实机验收

用户已分别确认两次接入的电源没有负载，并授权对应的开发验收控制测试。专门测试入口支持明确指定设备，默认仍为IT6722以兼容原命令；执行时按实际设备选择模板：

```sh
./dist/platform-linux-amd64 hardware-test --run \
  --device itech-it6722 \
  --port /dev/ttyUSB0 \
  --report ./docs/evidence/itech-hardware.json
```

```sh
# 龙威：先只读核对，再执行本次已授权的空载控制测试
./dist/platform-linux-amd64 hardware-test --device longwei-lw3010e \
  --read-only --port /dev/ttyUSB0 --address 1 \
  --report ./docs/evidence/longwei-readonly.json
./dist/platform-linux-amd64 hardware-test --device longwei-lw3010e \
  --run --port /dev/ttyUSB0 --address 1 \
  --report ./docs/evidence/longwei-hardware.json
```

`--read-only`只执行三个读取服务，不发送控制命令；报告默认按设备类型区分，不覆盖另一款设备证据。控制测试保存初始状态，执行5V/0.5A、开启输出、改9V、改1A、关闭输出，每个观察阶段约8秒；结束显式恢复初始设定及输出状态，IT6722另发送 `SYST:LOC`，龙威不发送SCPI命令。初始设定无法在设备边界内恢复时，在任何控制前拒绝测试。此恢复行为只存在于测试工具。默认单元测试、浏览器测试和服务启动不会运行该入口。恢复无法确认会返回非零状态并在证据中说明。

两款电源的新Go实现均已完成空载实机验收。龙威2026-11-03测试后恢复12V/5A、输出开启，并用重新打开串口的只读查询复核。空载只验证电流限值设定和回读，不代表带载限流或独立仪表校准通过。

## 部署、升级与备份

[systemd样例](deploy/platform.service) 假设二进制放 `/opt/devconsole/platform`，配置在 `/etc/devconsole/config.yaml`，数据目录设为 `/var/lib/devconsole`。按实际环境创建专用用户、目录及串口权限，然后自行安装服务文件。仓库不会自动修改系统服务、防火墙或全局配置。手机或其他电脑应在可路由网络内访问服务器局域网地址。

存活检查为 `GET /healthz`，单台设备离线不会让平台检查失败。

停机备份和恢复：

1. 正常停止平台进程，确认退出。
2. 备份整个数据目录及配置，保留权限；不要只复制运行中的SQLite主文件。
3. 升级时替换二进制，保留数据目录，启动后自动检查数据库版本。当前数据库版本为1，不支持读取更高版本数据库。
4. 恢复时先停进程，恢复整份备份及配置，再用匹配版本的程序启动。不要将新旧数据库文件或WAL混合。
5. 检查登录、项目、Token和绑定；启动只读探测，不会重放未完成调用。服务中断时未终结记录标为结果未知。

调用记录当前不自动清理，需留意数据目录增长。停机备份会暂时中断服务，第一版不提供在线备份和滚动升级。

模块依赖、存储代价及执行边界见 [架构说明](docs/architecture.md)。具体通过项目、证据与未验证边界见 [验收报告](docs/verification.md)。原始需求和协议资料保留在仓库外的 `../solution/`。
