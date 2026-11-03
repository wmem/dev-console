# 第一版实施与验收报告

首次验收日期：2026-11-02；2026-11-03补充龙威实机复验、串口稳定绑定、安装与端口修复，以及0.2.0可恢复Token和完整Agent说明。实施和约定的本机验收已完成，外部验证边界单独列出。

## 已实现

- Go/Vue单程序、嵌入本地页面与资源、SQLite事务迁移及持久化，Linux/amd64、CGO_ENABLED=0构建。
- 通用设备与设备服务、项目及项目服务契约，共绑、兼容改绑、稳定URL、绑定失效处理。
- 两款电源配置模板、多实例、共享Modbus串口、非阻塞busy、设置回读、超时结果未知处理；IT6722记忆并校验序列号，拒绝同型号未知替换设备。
- 本机管理员初始化、用户角色与禁用、项目成员、登录改密、Session撤销、Cookie CSRF、单项目Token作用域及有效期和撤销。
- 确定性Markdown与OpenAPI、契约驱动表单及校验、调用开始与结果审计、分页筛选、revision冲突和删除引用保护。
- 中文桌面/手机页面，设备、项目、服务、调用、文档、Token、用户与记录完整流程；局域网HTTP复制回退，页面内影响确认。
- 单命令构建、配置和systemd样例、停机备份恢复说明、独立模拟器及可复现测试。

## 构建与自动化

实际工具链：Go1.26.3、Node24.15.0、npm11.12.1。产物 `dist/platform-linux-amd64` 为x86-64静态链接ELF。

| 检查 | 结果 | 证据 |
|---|---|---|
| `npm run build -- --target linux/amd64` | 通过；npm ci、Vue类型检查、Vite生产构建、CGO_ENABLED=0编译 | [构建日志](evidence/build-and-race.txt) |
| `go test -race -json ./...` | 60个测试项通过，含子用例；7个有测试包通过，无失败及数据竞争报告 | [结构化结果](evidence/go-test-report.json)、[串口绑定回归日志](evidence/serial-binding-regression.txt) |
| `go vet ./...` | 通过 | [检查日志](evidence/build-and-race.txt) |
| `npm run test:runtime` | 通过；无Node/Go工具链PATH运行最终程序，验证停机备份副本恢复、Session/Token/绑定、设备身份及离线状态、SPA/API边界 | [运行与恢复结果](evidence/runtime-report.json) |
| `npm run test:hardware-cancel` | 通过；伪终端模拟器上发送SIGINT，确认恢复初始状态并非零退出 | [中断恢复结果](evidence/hardware-cancel-report.json) |
| `npm run test:hardware-cancel -- longwei-lw3010e` | 通过；龙威只读模式无06写入，SIGINT中断后恢复初始状态 | [龙威只读与中断恢复模拟](evidence/longwei-hardware-cancel-report.json) |

自动化覆盖小数精度与默认参数、契约/文档一致性、共绑与改绑、revision、权限与CSRF、Token撤销、密码重置与会话失效、最后管理员保护、共享总线busy、状态上报完成前拒绝配置更新、独立串口并行、取消清理期间保持busy、删除后不复活、审计写入失败不控制设备、历史帧解码、CRC、异常功能码/地址/字节数、分段回复、超时断开、SCPI科学计数法/乱码/空行/超长回复/错误队列、设备身份替换拒绝等。

历史原始记录保留在 [testdata/history](../testdata/history/README.md)，是独立外部协议依据，不替代新Go代码的实机验收。

## 页面验收

使用已有 `playwright-cli` 0.1.18 和 Chromium **152.0.7977.8**：

```text
/home/mem/work/software/playwright-browsers/chromium-1237/chrome-linux64/chrome
```

| 模式 | 视口 | 触摸模拟 | 完整流程 | 意外控制台错误 / 页面异常 |
|---|---|---|---|---|
| 桌面 | 1440×900 | 否 | 通过 | 0 / 0 |
| 手机 | 390×844 | 是 | 通过 | 0 / 0 |
| 窄屏手机 | 360×800 | 是 | 通过 | 0 / 0 |

三个模式均覆盖登录退出、用户创建/重置/禁用、设备创建/配置/只读探测、稳定串口候选选择及路径持久化、原设备重选串口后保留ID/项目绑定/原Token调用、项目服务绑定、参数范围拦截、操作回读、实时采集、busy提示、显式限频刷新、隐藏事件停止刷新、Markdown/OpenAPI下载、复制回退、Token创建及重复取回/完整Agent说明/旧原文补存/调用/撤销、成员权限、记录筛选与绑定快照、离线提示、本人改密与会话撤销、深链接刷新和横向溢出检查。

测试访问最终可执行程序提供的嵌入页面和真实API，设备端为独立伪终端模拟器。地址为当前服务器局域网IP `192.168.252.200:18480`。预期的未登录401、busy409和离线503单独记录，不当作意外错误。

实际截图已用模型视觉检查，中文、布局、参数及结果、窄屏弹窗、错误提示和文档显示正常。宽表格在组件内横向滑动，整个页面没有横向溢出。

- [结构化页面结果](evidence/browser-report.json)
- [桌面服务页](evidence/desktop-services.png)
- [手机调用与回读](evidence/mobile-invoke.png)
- [360px手机服务页](evidence/mobile-narrow-services.png)
- [手机离线错误](evidence/mobile-narrow-offline.png)
- [手机项目文档](evidence/mobile-docs.png)

测试凭据只用于一次性环境，截图不保留Token原文或密码；结束清理临时数据库、脚本和浏览器会话。本机原生沙箱限制通过本项目临时启动配置 `chromiumSandbox:false` 处理，没有下载浏览器或修改全局配置。

## IT6722实机验收

按用户已给出的空载授权，对 `/dev/ttyUSB0` 上真实IT6722运行最终构建的测试工具，完成19项操作/读取，所有设置均通过对应回读确认。

| 项目 | 结果 |
|---|---|
| 初始身份与状态 | 型号、序列号查询成功，初始12.98V / 9.01A、输出关闭 |
| 关闭输出、设置5V / 0.5A | 回读确认 |
| 开启输出、停留约8秒 | 状态回读确认，并读取实测V/I |
| 改9V、停留约8秒 | 设定值回读确认，并读取实测V/I |
| 改1A、停留约8秒 | 限值回读确认，并读取实测V/I |
| 关闭输出 | 状态回读确认 |
| 恢复初始状态 | 最终独立读取确认12.98V / 9.01A、输出关闭 |
| 结束控制 | 已发送SYST:LOC并释放串口；未使用独立面板查询证明本地接管状态 |

[完整实机证据](evidence/itech-hardware.json) 包含UTC时间、结果、确认阶段、原始TX/RX及恢复状态。最终记录时间为2026-11-02T14:34:20Z。空载仅验证电流限值设置和回读，不代表带载恒流、额定功率或外部仪表精度通过。

## 龙威LW-3010E实机复验

2026-11-03，用户确认龙威当前接入 `/dev/ttyUSB0`、空载并允许完整测试。使用新增的设备选择及只读选项，经同一生产执行链完成复验；生产驱动与页面未改动，Linux/amd64可执行文件已重新构建，未重复无关的页面验收。

连接为9600、8N1、Modbus地址1。该协议未验证可靠型号/序列号查询，型号依据用户确认，不能将寄存器回复当作唯一物理身份凭据。

| 阶段 | 结果 |
|---|---|
| 初始只读核验 | 12V / 5A、输出开启、CV；实测12.01V / 0A，仅发送03查询 |
| 关闭输出并设5V / 0.5A | 06回显及03回读通过 |
| 开启输出、停留约8秒 | 输出状态确认，实测4.99V / 0A |
| 改9V、停留约8秒 | 设定值确认，实测8.99V / 0A |
| 改1A、停留约8秒 | 限值确认，实测8.99V / 0A |
| 关闭输出及恢复 | 恢复12V / 5A、输出开启，设置和状态回读一致 |
| 关闭测试连接后独立复核 | 重新打开串口，只读确认初始设定及输出开启、CV状态一致 |

完整控制流程记录19项操作/读取及原始TX/RX，所有设置均通过回读确认。证据为 [初始只读](evidence/longwei-readonly.json)、[完整控制与恢复](evidence/longwei-hardware.json)、[恢复后的独立只读复核](evidence/longwei-post-test-readonly.json)。本次补齐龙威新Go接入的实机复验，仍未验证带载限流、满功率或独立仪表精度。最终设备输出保持测试前的开启状态。

## 稳定绑定与重新选择串口

新增只读串口候选API与页面，区分唯一USB序列号、固定插口和手动路径。配置保存稳定链接；每次连接解析实际节点，编号变化时安全迁移共享连接；身份不可靠、重复、变更或配置冲突时明确拒绝，不擅自挑选设备。

60项自动化中包含ttyUSB/ttyACM独立sysfs夹具、唯一身份和固定插口链接改指向、重启恢复、busy期间迁移拒绝、别名重复、USB身份重复/缺失/变化、打开期间替换、离线身份保留、动态冲突不阻断平台启动且可编辑修复，以及HTTP原设备换端口后继续使用原设备ID、服务绑定、URL和Token。IT6722仪器序列号保护仍然保留。

桌面1440×900、手机390×844与360×800均验证配置页面和原Token重选后调用。截图复核后将串口路径置于表单首项，并修复手机底部按钮遮挡；[手机重选页面](evidence/mobile-serial-reselect.png)、[窄屏重选页面](evidence/mobile-narrow-serial-reselect.png)、[桌面重选页面](evidence/desktop-serial-reselect.png) 已保存。

当前CH340没有唯一USB序列号，泛化by-id候选被禁用，固定插口候选可用。真实龙威通过 `/dev/serial/by-path/pci-0000:80:14.0-usb-0:6:1.0-port0` 完成三个只读服务，只发送03查询，设定12V/5A、输出开启、CV；见 [稳定路径只读证据](evidence/serial-binding-hardware-readonly.json)。本次未物理拔插或实际制造编号变化，重枚举由文件系统、sysfs及协议模拟器验证。详细操作与升级说明见 [串口绑定说明](serial-binding.md)。

## 未验证与边界

- 两款电源均已通过新Go实现的空载实机测试，带载限流、满功率及外部仪表精度未验证。
- 手机为Chromium移动视口和触摸模拟，未使用实体手机，未宣称Safari/iOS或其他引擎已验收。
- 早期页面流程为本机浏览器访问实际局域网IP；后续已从另一台机器对N4505部署执行HTTP只读复核，结果见下文。实体手机和完整跨终端登录/调用流程仍未验收。
- 仅验收Linux/amd64，未交付或宣称验证Linux/arm64和Windows。
- systemd模板与用户级启动已验证，用户提供N4505上0.1.1的root安装与enabled/active状态；当前0.2.0的目标机root升级、重启后的自启动、串口组权限仍未实测，未调整防火墙。
- 数据库格式版本为2；真实旧程序格式1到2的迁移及失败恢复已在临时目录验证，详情见下文，未在目标部署执行。未提供在线备份、高吞吐定时采集、历史自动清理或多进程串口仲裁。

启动与首次登录见 [README](../README.md)，模块边界及存储代价见 [架构说明](architecture.md)。上述自动化验收的临时环境已清理；部署时应按README初始化管理员及数据目录，验收记录不作为部署实例的当前运行状态。

## 版本与安装包补充验收（2026-11-03）

支持范围从原来保留Windows兼容收敛为仅Linux/amd64，版本统一由根目录VERSION维护。`npm run build`编译单程序，`npm run package`复用构建生成 `dist/dconsole-0.1.0-install.run`，发布与恢复步骤见[发布与安装](installation.md)。

安装验证使用真实自解压包和三个不同版本标记的当前代码构建、真实HTTP及SQLite。特权账号、chown和系统级systemctl在事务测试中替代，验证首次初始化失败、首次服务失败后的修复、重复安装、保留配置/服务定义/项目/设备ID/绑定/Session/Token的升级、同版本补登记、降级拒绝、备份失败、健康失败回退、格式变化后完整数据恢复，以及新服务无法停止时禁止覆盖数据。

另外在真实用户级systemd中创建临时服务，验证实际程序启动、版本匹配的健康检查、管理员登录、嵌入页面、目录内日志和停止清理；系统级模板通过systemd-analyze检查。该结果不等于系统级User/Group、SupplementaryGroups、ProtectSystem或开机启动已通过，当前无免密sudo且用户命名空间受限，未执行root部署。安装过程未操作真实设备或已有体验服务。

安装包、事务与用户级systemd的逐项结果和限制见[结构化证据](evidence/install-report.json)。普通安装回归中的三个版本标记使用相同存储实现，模拟格式变化用于验证恢复分支；0.2.0另用真实旧格式程序验证实际迁移，见下文。新增验证没有改写前面的历史实机记录。

最终补充检查：`go test -race -json ./...`通过78项基础测试，12个有测试包通过，无失败或数据竞争；[基础测试结构化结果](evidence/package-go-test.json)保留两个安装测试的条件跳过记录，对应专门脚本已提供真实程序后另行通过。`npm run test:install`通过33项检查，含完整事务、正式发布门禁、端口配置及用户级systemd；`npm run test:token-upgrade`、`go vet ./...`、ShellCheck与修改脚本的Node语法检查通过，`npm run test:runtime`在0.2.0重新通过。当前安装包为0.2.0开发构建，内嵌dirty=true，尚未创建正式发布标签或执行该版本目标机root升级。

首次安装端口补充：交互默认8080，非法和占用端口重输；--port仅首次可用，非交互必须指定，配置写入所选端口，升级/修复保留原值。测试覆盖参数范围、真实端口占用在账号/目录准备前拒绝、不预取下一行密码、配置写入和已有实例参数拒绝；低端口仅增加CAP_NET_BIND_SERVICE的服务配置边界已检查。真实系统级低端口能力生效仍需目标机root验收，未以配置检查冒充权限实测。

0.1.1权限预检修复：根据实际/opt属主及写权限导致的安装阻断，取消安装器对已有路径root属主和不可写模式的要求，不修改/opt权限。新增可写路径检查回归，并在非root属主、0777父目录下执行同一首装事务，升级时以0777安装目录和0666配置/服务定义验证继续执行及权限保留。目录类型、原子替换、数据保留和失败恢复仍按原流程验证；这不等于已在目标机器执行root安装。

## 0.1.2浏览器端口修复与部署诊断

用户在N4505选择10080安装0.1.1后，服务为enabled/active，但网页打不开。2026-11-03从192.168.252.200只读访问192.168.252.195，healthz及首页均HTTP200，确认服务和跨终端HTTP链路可达；Fetch在发请求前返回bad port。10080位于[WHATWG Fetch端口禁止表](https://fetch.spec.whatwg.org/#port-blocking)，无需据服务状态猜测或修改防火墙。[远端诊断证据](evidence/remote-port-diagnosis.json)保留了版本、提交及10080/10081的复核结果，未执行远程配置、重启、登录或硬件操作。

使用指定目录内Chromium152.0.7977.8及playwright-cli，独立HTTP对照服务在10080返回200，浏览器仍报net::ERR_UNSAFE_PORT；最终Go程序在可访问端口显示登录页，桌面1440×900和手机390×844触摸模拟通过，健康接口200。[浏览器结果](evidence/browser-port-report.json)、[桌面截图](evidence/browser-port-desktop.png)、[手机截图](evidence/browser-port-mobile.png)已保存并视觉复核。本次仅验证页面可访问，没有执行登录或设备操作。

0.1.2在首次端口输入和实际监听预检中拒绝禁用端口，并校验public_base_url的外部端口。代理的内部10080配合安全外部URL仍允许；配置回归覆盖这一边界。安装回归确认禁用端口在账号/目录准备前拒绝、错误的旧外部URL在升级停止服务前拒绝。已有部署的10080须先按[恢复步骤](installation.md#服务运行但浏览器无法访问)改为10081等端口，安装器不自动改写原配置。

## 0.2.0可恢复Token与Agent交接

用户明确要求把原“仅保存摘要、原文只展示一次”改为可恢复加密存储。实现采用AES-256-GCM、随机nonce及项目/Token ID绑定的附加认证数据，保留摘要鉴权；只有管理员Cookie与CSRF校验后的显式接口能够取回原文。普通列表和项目文档不返回原文或密文，旧Token补存须先匹配原摘要，详见[保存与恢复说明](tokens.md)。

基础回归覆盖重复取回、重启恢复、密文损坏/替换、错误密钥、密钥缺失不重建、公开元数据脱敏、项目隔离、成员/项目Token/缺CSRF拒绝，以及撤销和过期后的拒绝。完整运行回归用停机数据副本恢复登录、绑定及原Token，并再次取回相同原文，见[运行与恢复证据](evidence/runtime-report.json)。

`npm run test:token-upgrade`使用归档0.1.2安装包内数据库支持范围1～1的真实程序，创建旧数据后交给当前支持1～2的程序及安装事务：迁移后注入健康失败，验证恢复旧程序与格式1数据，并将新密钥随failed-data保留；成功升级时验证旧Token继续鉴权、补存同一原文和新Token加密取回。停机副本包含密钥；删除密钥时启动失败且不生成新密钥，恢复配套密钥后可取回原值；真实旧程序拒绝格式2。逐项结果见[跨格式升级与密钥恢复证据](evidence/token-upgrade-report.json)。这些检查使用临时目录、真实程序/SQLite/HTTP，系统账号、chown和系统级systemctl仍为替代实现，未在目标机root升级。

指定目录Chromium及playwright-cli重新完成桌面1440×900、手机390×844和窄屏360×800全流程。三个视口均验证新建后完整说明包含真实Token/地址/Bearer/curl、关闭后再次取回相同原文、文档页入口、局域网HTTP手动复制回退、旧Token补存后继续用原凭据；无意外控制台错误或页面异常。[页面结构化证据](evidence/browser-report.json)、[桌面完整说明](evidence/desktop-agent-guide.png)、[窄屏完整说明](evidence/mobile-narrow-agent-guide.png)及[手机旧Token补存后说明](evidence/mobile-legacy-token-guide.png)已保存并视觉复核，截图中的测试Token已脱敏。

`npm run package`已生成Linux/amd64的0.2.0安装包；`test:install`、`test:token-upgrade`、`test:browser`及`test:runtime`均在该构建上通过。本次未操作真实硬件或更改已有体验服务，也未宣称完成目标机root升级、实体手机或其他浏览器引擎验收。
