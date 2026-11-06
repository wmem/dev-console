# 米家 App 协议实施记录

调研时间：2026-11-06。范围：中国大陆账号、扫码登录、家庭/设备发现、MIoT 标准布尔开关。这里记录南向协议来源，不维护第二套项目服务参数或默认值；后者来自 `switchServices` 并由运行时生成文档。

协议事实参考：

- [Do1e/mijia-api](https://github.com/Do1e/mijia-api/blob/main/mijiaAPI/apis.py)：`sid=mijia` 的账号扫码、登录态续期、`api.mijia.tech/app` 下的家庭和设备分页、MIoT 属性接口。其 [2026-10-10 规格迁移说明](https://github.com/Do1e/mijia-api/blob/main/docs/changelog.md) 要求使用设备返回的 `spec_type`。该仓库声明 GPL-3.0，本实现不将其客户端或资源作为依赖、复制或翻译其源码。
- [Squachen/micloud 的 miutils.py](https://github.com/Squachen/micloud/blob/master/micloud/miutils.py)：MIT 文件公开了 nonce、SHA-256 派生、SHA-1 签名和 RC4-drop1024 的互操作格式。本目录按协议格式独立编写 Go 实现，不引入其 Python 代码或依赖。
- [smathsp/mijia-api](https://github.com/smathsp/mijia-api)：调研 Go 单程序实现的可行性；不复制或链接其 GPL 客户端。
- [MIoT 规格接口](https://miot-spec.org/miot-spec-v2/instance)：按设备返回的完整规格 URN 查询；只认可标准 `on`、`bool` 属性及明确的 read/write 权限。

实现对照：`client.go` 负责短请求、后台扫码及有界解析，`crypto.go` 负责协议互操作，`devices.go` 负责发现、规格白名单及属性响应关联，账号与设备生命周期分别位于 `accounts.go`、`manager.go`。不借用 HA 专用 client_id、回调身份、MQTT 或 API。

独立模拟器位于 `internal/testxiaomi`，自己解析签名并编码加密响应，不调用生产编解码。夹具全为人工构造；登录凭据、二维码、型号和设备标识均不代表真实小米账号或硬件。

本记录不表示小米提供公开稳定的第三方 API 合同，也不表示完成实机验收。接口变更、海外地区、验证码、网关和具体型号仍需按实际情况验证。
