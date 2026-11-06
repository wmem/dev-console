# 历史协议证据

本目录保留2026-11-02历史Windows环境的原始串口收发JSON，作为独立协议夹具。COM31/COM32只代表当时连接，不是当前Linux端口。原始内容和换行保持不变，不作为运行配置。

| 设备 | 历史只读 | 历史控制 | 协议说明 |
|---|---|---|---|
| 龙威LW-3010E | [longwei-readonly.json](../../docs/development/acceptance/evidence.mdoc#device:file:testdata:history:longwei-readonly) | [longwei-controls.json](../../docs/development/acceptance/evidence.mdoc#device:file:testdata:history:longwei-controls) | [Modbus协议](../../docs/usage/reference/protocols/longwei-lw3010e.mdoc) |
| ITECH IT6722 | [itech-readonly.json](../../docs/development/acceptance/evidence.mdoc#device:file:testdata:history:itech-readonly) | [itech-controls.json](../../docs/development/acceptance/evidence.mdoc#device:file:testdata:history:itech-controls) | [SCPI协议](../../docs/usage/reference/protocols/itech-it6722.mdoc) |

这些历史记录不替代当前Go实现的实机验收。两款电源的新实现均已完成空载实机测试，证据分别为 IT6722（`docs/evidence/itech-hardware.json`）和 [龙威](../../docs/development/acceptance/evidence.mdoc#device:file:docs:evidence:longwei-hardware)；龙威另有 [恢复后独立只读复核](../../docs/development/acceptance/evidence.mdoc#device:file:docs:evidence:longwei-post-test-readonly)。实际测试范围和未验证项集中见 [验收报告](../../docs/development/acceptance/implementation.mdoc)。
