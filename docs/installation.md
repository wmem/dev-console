# 版本、打包与安装

仅支持Linux/amd64。构建机也须为Linux/amd64，使用[README](../README.md)列出的Go/Node工具链；安装目标不需要Go、Node、Python或联网获取依赖。目标需运行systemd 240或以上，具备Bash、gzip、tail、mktemp等基础命令及systemctl、useradd、runuser，安装使用root权限。

## 编译与打包

```sh
# 仅编译，生成dist/platform-linux-amd64
npm run build

# 调用同一构建流程，再生成dist/dconsole-${version}-install.run
npm run package
```

`build -- --target linux/amd64`保留兼容，其他目标拒绝。`package`每次重新构建，避免打包旧程序；包名版本与程序内嵌版本必须一致。包内只有gzip压缩的静态可执行程序，前端、安装器与服务模板均已嵌入。运行文件不随源码提交。

```sh
# 在不安装的情况下查看包内版本，或解压到一个不存在的目录
bash dist/dconsole-0.2.0-install.run --version
bash dist/dconsole-0.2.0-install.run --extract ./unpacked
./unpacked/dconsole version
```

普通打包允许未提交修改，版本信息中dirty=true；这种产物用于开发验收，不作为可追溯的正式发布。gzip校验可以发现传输损坏，不提供发布者身份认证，应从可信发布渠道取得安装包。

## 应用版本维护

[VERSION](../VERSION)是应用版本唯一来源，采用不带v前缀的 `主版本.次版本.修订版本`，目前只接受三个非负整数，不接受预发布后缀。前端不单独维护应用版本。

- 主版本：不兼容接口、配置或使用行为变化。
- 次版本：兼容的新功能。
- 修订版本：兼容的缺陷修复。

版本比较按数值逐段进行，0.1.10大于0.1.9。二进制的 `version`命令输出JSON，包含version、完整Git提交、dirty、target及支持的数据库格式范围；启动日志和healthz也报告版本及提交。安装目录不另存一份可漂移的版本文件。

数据库格式版本由[存储实现](../internal/storage/storage.go)维护，服务contract_version由[设备服务定义](../internal/access/power/power.go)维护，两者均不随应用版本机械递增。当前数据库格式为2，支持范围1～2，首批服务契约为1；0.2.0启动时迁移格式1，为Token增加独立密文表及配套密钥，旧摘要Token继续有效。回退0.1.x须恢复升级前的整份数据，不能只替换二进制；详见[Token保存与恢复](tokens.md)。后续迁移须同样明确升级顺序、数据兼容性和恢复要求，并增加对应测试。

正式发布流程：

1. 修改VERSION和[CHANGELOG](../CHANGELOG.md)，记录行为、配置、数据兼容性及升级要求。
2. 执行构建、回归与安装验证，确认变化所涉及的边界；前端变化另外按README执行浏览器验收。
3. 提交全部源码和文档，在该提交创建 `v${version}` 标签。
4. 执行 `npm run package -- --release`。该命令要求工作区干净、对应标签指向当前提交、CHANGELOG有当前版本，构建后再次检查版本和工作区。命令本身不创建标签、不提交或推送。
5. 将安装包与该版本说明一起交付。安装同版本默认不替换，正式版本发布后不要以相同版本号分发不同内容；修复应递增修订版本。

## 首次安装

把run文件复制到目标机器后：

```sh
sudo bash dconsole-0.2.0-install.run

# 首次安装也可以显式指定端口，跳过端口提问，仍需设置管理员密码
sudo bash dconsole-0.2.0-install.run --port 9000

# 串口需要补充组权限时，先查看实际节点所属组，再明确传入，例如：
ls -l /dev/ttyUSB0
sudo bash dconsole-0.2.0-install.run --serial-group dialout
```

`dialout`仅为示例，安装器不会猜测或修改设备节点权限。指定组必须已存在。模板不隐藏/dev，因此ttyUSB、ttyACM、by-id和by-path均可按原有规则使用；不会创建udev规则。

安装器检查平台、工具和systemd，首次先提示 `设置HTTP监听端口 [8080]`，回车使用8080；非法或已占用端口提示后重输，也可用--port跳过提问。端口范围1～65535，先检查能否监听，再创建账号和暂存安装；预检不会关闭占用端口的其他进程。预检与实际启动之间仍有时间窗口，最终是否可用以服务启动和健康检查为准。

端口范围还排除[WHATWG Fetch规定的浏览器禁用端口](https://fetch.spec.whatwg.org/#port-blocking)，例如10080、6000和6667。此类端口可能可监听、curl也返回200，但浏览器会在发出请求前拒绝，Chromium报ERR_UNSAFE_PORT；安装器会提示改选8080、9000或10081等可访问端口。

端口确定后创建无登录shell的dconsole专用用户及同名组，暂存程序、配置和服务模板，以该用户身份交互设置admin密码，无默认密码；成功后才将目录正式放到 `/opt/dconsole`、登记并启动服务、核对健康响应的版本/提交，最后启用开机启动。账号已存在时必须是普通用户、同名主组且主目录匹配，不能自动接管其他账号。所选端口写入config.yaml的listen，public_base_url留空时自动生成的访问地址也使用该端口。

低于1024的端口在首次生成服务定义时仅授予CAP_NET_BIND_SERVICE，并将能力上限限制为这一项，仍以普通dconsole用户运行；1024及以上不额外授予该能力。系统级低端口权限仍需目标机验收。

非交互初始化须显式指定 `--port <端口> --password-stdin`，由标准输入提供一行密码；缺少端口时在创建账号或安装目录前报错，不把密码当作端口读取。交互端口输入也不预取下一行密码。不要把密码写在命令行或版本文件中。升级不提问端口、不读取该密码、不重新初始化管理员。

```text
/opt/dconsole/
├── dconsole              # root拥有，0755
├── config.yaml           # root:dconsole，0640，保存首次所选局域网监听端口
├── dconsole.service      # root拥有；升级保留原文件
├── data/                 # dconsole拥有，0700，数据库文件0600
│   ├── platform.db       # SQLite及可能存在的WAL/SHM文件
│   └── token.key         # Token加密主密钥，新建文件0600，随数据库配套备份
├── logs/                 # dconsole拥有，0700
│   └── dconsole.log
└── backups/              # root拥有，0700，升级时创建
```

systemd登记链接为 `/etc/systemd/system/dconsole.service → /opt/dconsole/dconsole.service`，开机启动链接和账号记录由操作系统管理。安装并发锁位于系统运行目录 `/run/lock/dconsole-install.lock`，进程结束即释放；它不是应用配置。应用程序、配置、数据库、日志及备份均放在/opt/dconsole。

安装器不检查已有父目录、安装目录、程序、配置、服务定义或备份的属主和权限模式，不因/opt属于普通用户或可被组/其他用户写入而拒绝，也不会修改/opt的权限。新创建文件和数据目录仍设置上述默认权限以保证服务可运行；升级保留已有目录、配置和服务定义的权限。目录类型、符号链接及同名系统服务冲突仍按原流程检查，服务的systemd运行方式保持不变。

```sh
sudo systemctl status dconsole
sudo systemctl restart dconsole
sudo tail -n 100 /opt/dconsole/logs/dconsole.log
/opt/dconsole/dconsole version
```

浏览器访问 `http://<目标机器IP>:<所选端口>`，账号为admin，密码为安装时设置的值。配置和端口修改后重启服务；后续从高端口改为低端口还需同步服务能力配置并daemon-reload。应用日志直接追加到目录内文件；systemd自身的管理/启动失败诊断仍可用 `journalctl -u dconsole`查看，当前没有自动日志轮转，应按部署需要安排保留策略。

已有开发目录的体验服务不会被安装器自动迁移。指定端口已占用时预检拒绝，交互模式可重新选择；其他进程在预检后抢占端口导致首次启动失败时保留现场供处理，不会停止其他服务或猜测导入其数据库。

## 升级与修复

执行较新版本的同类安装包即可：

```sh
sudo bash dconsole-0.2.1-install.run

# 同版本重复执行默认无操作；显式修复程序/服务登记：
sudo bash dconsole-0.2.0-install.run --reinstall
```

升级在停止服务之前检查版本、配置、固定数据目录与数据库格式。默认拒绝降级；配置的data_dir须解析到/opt/dconsole/data，不能通过安装器把数据放到目录外。升级流程为：正常停止服务 → 备份旧程序、配置、服务定义及整个数据目录 → 同文件系统原子替换二进制 → 启动 → 核对版本/提交与健康状态 → 确认运行并启用开机启动。

正常升级不覆盖config.yaml、dconsole.service和data，不重建管理员或清空业务数据；数据库可能由新程序按独立迁移规则更新，不能把“保留数据”理解为SQLite文件字节完全不变。原设备ID、项目、绑定、Token与登录Session保留；重启仍只读探测，不恢复历史电源输出。

--port仅用于首次安装，已安装实例传入该参数会明确拒绝，包括同版本和--reinstall。需要改端口时由管理员编辑config.yaml并重启，不通过升级参数覆盖原配置。

修复要求原程序仍可读取版本，避免在版本未知时绕过降级判断。若程序丢失或损坏到无法查询版本，应先从完整备份恢复对应旧程序，再执行修复；--reinstall不会据数据库格式猜测应用版本，也不重写现有服务定义。

升级保持现有服务定义，不能用 `--serial-group`覆盖。需调整串口组时，由管理员修改/opt/dconsole/dconsole.service中的SupplementaryGroups，再执行 `systemctl daemon-reload`和重启。未来版本如果必须改变服务定义或配置格式，应单独说明迁移步骤，不能偷偷在普通二进制升级中覆盖。

每次实际升级/修复保留一个 `backups/<UTC时间>-<原版本>-<唯一后缀>/`，含旧程序、配置、服务及停机数据。备份不自动清理，需要按空间和保留策略管理。备份复制失败时不替换原程序，并尝试重新启动旧服务；可读写文件系统及足够备份空间是升级前提。

0.2.0起，加密Token的恢复依赖data/token.key，手工备份也必须包含整个数据目录。已有密文而密钥缺失时程序拒绝启动，不会生成替代密钥；恢复错误密钥不能解密。不要把其他实例的密钥与当前数据库混用。仅有摘要的旧Token无法从摘要还原，可在页面补存已持有的原文。

## 失败与恢复

初始化管理员失败时清理暂存目录，不登记服务。首次服务启动失败时保留已初始化数据，停止本次服务并返回非零；处理日志中的原因后用同版本 `--reinstall`继续，不能删除数据库重新初始化。

升级后启动或健康检查失败时先确认停止新服务。数据库仍在旧程序支持范围内则恢复旧二进制；格式已变化或无法检查时，同时恢复整份升级前数据，变更后的数据保留在该备份的failed-data目录。恢复后重新启动并核验旧版本，原升级命令仍返回非零，明确说明升级失败及恢复结果。

配套数据恢复使用Linux原子目录交换，交换失败时原数据目录仍在原位；文件系统不支持该操作或目录跨挂载点时返回恢复失败并保留两份数据，转人工处理，不先移走原目录留下空路径。

如果无法停止新服务、磁盘损坏或恢复步骤失败，停止自动恢复并给出备份位置，不声称已经恢复。断电和SIGKILL不能保证执行清理；应先停服务并检查目录、版本、日志及完整备份，再选择修复或人工恢复。安装器不提供强制降级参数。

人工从完整备份恢复时：确认服务停止 → 保存当前现场 → 将旧程序以0755权限放回，必要时恢复配套配置/服务 → 恢复整个数据目录并设置dconsole所有权及目录0700/文件0600 → daemon-reload并启动 → 核对登录、Token、绑定和版本。不能混用新旧主库及WAL，不能在新服务仍运行时复制旧数据库覆盖。

## 验证与限制

```sh
npm run package
npm run test:install
npm run test:token-upgrade -- --legacy-package ./dist/dconsole-0.1.2-install.run
npm run test:race
go vet ./...
npm run test:runtime
npm run test:browser-port
```

安装验证使用真实run解压、三个不同版本标记的真实程序、HTTP与SQLite，在临时目录执行同一安装事务，验证首次端口输入/范围/占用与配置保存、输入不预取密码、升级端口保留、首次失败/修复、重装、降级拒绝、备份失败、健康失败及格式变化恢复；不操作真实设备。特权账号、所有权及系统级systemctl使用替代实现，另通过实际用户级systemd验证启动、登录、页面与日志。后者不能替代系统级身份、权限隔离、低端口能力和开机启动验收。

test:token-upgrade需要另行保留的格式1旧安装包，默认使用dist/dconsole-0.1.2-install.run，也可显式传入路径；不会从当前源码伪造旧格式实现。该检查用真实旧程序创建数据，验证迁移到格式2、失败后的二进制与完整旧数据恢复、旧Token补存、配套密钥备份恢复，以及旧程序拒绝新格式。证据见[跨格式升级验证](evidence/token-upgrade-report.json)。

自动验证环境无免密sudo且不允许用户命名空间。用户另已在N4505完成0.1.1的root安装，并提供systemd enabled/active状态，跨终端HTTP200已独立复核；当前0.2.0的目标机root升级、重启后的自启动、低端口能力和真实串口组权限仍未实测。完整证据见[安装验证结果](evidence/install-report.json)和[验收报告](verification.md)。

## 服务运行但浏览器无法访问

先区分HTTP服务和浏览器限制：systemd active只说明进程运行；安装时健康检查成功也不代表浏览器允许该端口。如果curl可返回200，而浏览器提示ERR_UNSAFE_PORT，应检查实际访问端口是否在标准禁用列表中，不需要反复start服务。

0.1.1及更早的安装器曾允许10080。已有实例无需删除或重装，在目标机将原监听端口改为可用端口并重启，例如：

```sh
sudo sed -i.bak '/^listen:/s/:10080/:10081/' /opt/dconsole/config.yaml
sudo systemctl restart dconsole
```

随后访问 `http://<目标机器IP>:10081`，原配置备份在config.yaml.bak，账号和数据库保留。如果手动设置过public_base_url并包含10080，也要同步修正；留空时会自动使用新的监听端口。改端口前确认新端口可用。

升级0.1.2不会擅自改写现有端口；已有禁用外部端口会在升级预检中明确报错，须先修正配置再升级，原服务不会因预检失败而被停止。若内部10080只供反向代理使用，显式设置浏览器可访问的public_base_url，例如 `https://console.example.test`，配置检查只限制外部访问端口，保留内部监听能力。
