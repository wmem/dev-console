# 历史协议证据

本目录保留2026-11-02历史Windows环境的原始串口收发JSON，作为独立协议夹具。COM31/COM32只代表当时连接，不是当前Linux端口。原始内容和换行保持不变，不作为运行配置。

| 设备 | 历史只读 | 历史控制 | 协议说明 |
|---|---|---|---|
| 龙威LW-3010E | [longwei-readonly.json](longwei-readonly.json) | [longwei-controls.json](longwei-controls.json) | [Modbus协议](../../docs/protocols/longwei-lw3010e.md) |
| ITECH IT6722 | [itech-readonly.json](itech-readonly.json) | [itech-controls.json](itech-controls.json) | [SCPI协议](../../docs/protocols/itech-it6722.md) |

这些历史记录不替代当前Go实现的实机验收。两款电源的新实现均已完成空载实机测试，证据分别为 [IT6722](../../docs/evidence/itech-hardware.json)和 [龙威](../../docs/evidence/longwei-hardware.json)；龙威另有 [恢复后独立只读复核](../../docs/evidence/longwei-post-test-readonly.json)。实际测试范围和未验证项集中见 [验收报告](../../docs/verification.md)。
