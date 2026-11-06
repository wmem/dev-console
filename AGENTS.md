# 开发与文档入口

Dev Console 是通用设备服务平台。先读根 README，再从 `docs/index.mdoc` 选择使用或开发路线。产品约定在 `docs/development/design/requirements.mdoc`，当前实现边界在 `docs/development/architecture.mdoc`，验证方法和结果从开发入口进入。

## 实现与验证

- 平台核心通过设备服务契约组织调用；具体串口协议留在接入模块。设备身份、连接路径与项目服务绑定分别维护。
- 保持 busy、结果未知、无自动重试及重连不恢复输出的执行语义。产品约定、当前实现和实际验收分别说明，不用改写证据代替修复。
- Markdown、OpenAPI 与表单来自同一服务定义；当前项目的参数和默认值以运行时生成文档为准，不手工维护第二套接口事实。
- 测试使用隔离数据、模拟器和已有浏览器。硬件操作需要匹配当前明确授权的目标与范围，历史验收中的授权不自动延续到新设备操作。
- 原始协议 JSON、实机报告和截图保留其来源及时间范围，新的结果不覆盖已提交历史证据。账号、Token、数据库与配套密钥不提交。

## edoc 文档维护

正式入口为 `docs/index.mdoc`；`docs/usage/` 服务使用者与接入者，`docs/development/` 服务维护者。各层 index 维护阅读路线和 directory-order，长期引用使用稳定 section ID。已有 Markdown 可以直接参与 edoc。

使用已安装的 edoc，执行 `npm run docs:check` 与 `npm run docs:build`。不重新安装全局工具，不因文档任务升级应用依赖或启动硬件测试。移动资料同步检查链接、命令和构建路径；`output/` 与 `.edoc-build/` 不提交。文档校验通过不代表应用或硬件重新验收。
