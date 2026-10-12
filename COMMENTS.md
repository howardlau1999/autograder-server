# autograder-server 未关闭审查意见

- 更新日期：2026-10-12。
- 已移除本轮修复并验证通过的 5 项回归、4 项遗留缺陷；以下为仍需决策或按需清理的原意见。
- 修复前对照版本：server `d402ae9` / web `214aace`；下列事项为修复后的剩余意见。

| 原编号 | 当前状态及待处理内容 |
| --- | --- |
| §3.14 | 注册接口仍通过 `AlreadyExists + EMAIL / USERNAME` 区分邮箱、用户名是否存在，见 `pkg/grpc/user.go:122`。需要决定错误反馈与账号枚举防护的取舍。找回密码对不存在邮箱已走空响应，不能继续笼统描述为同一种枚举问题。 |
| §12.7 | 小组提交 UI 尚未实现。`upload-dialog.component.ts:146` 仍只传当前用户；后端 submitters 校验已存在。 |
| §11.2 余项 | 会话令牌仍存 `localStorage`，见 `web/src/app/service/token.service.ts:21`、`:54`。CSP 没有改变令牌存储方式，Cookie 方案仍需鉴权架构调整。 |
| §12.2 | environment 仍硬编码公开 site key / client ID，并作为运行时配置缺失时的回退。是否完全改为服务端配置仍待定；它们不是秘密凭据。 |
| §13.2 | 本轮 lint 仍有 19 条 `no-explicit-any` 警告，0 error，可按具体 DOM/库回调逐步收紧类型。 |
| §7.13 / §7.14 | 长函数及 protobuf getter/字段访问混用仍有清理空间，属于可读性事项，未据此确认行为缺陷。 |

## 本轮验证

- Go：构建、`go vet -tags containers_image_openpgp ./...`、`go test -race -tags containers_image_openpgp ./...` 通过。
- 新增回归覆盖：源文件及生产构建的 Go 配置注入、错误模板拒绝、调度通知/候选切换/停机/取消/重排、关闭缓冲区的报告送达和重连、Pebble 数据生命周期、gRPC-Web 自定义 trailer，以及前端请求/倒计时的销毁与重启。
- 前端：74 项测试通过，生产构建成功；lint 0 error、19 条既有 `any` warning。
- Chromium：HTTPS 外站图片加载成功，内联脚本仍被 CSP 拦截；最小 VCD 波形和 ZIP 解压通过。
- CI：发布任务依赖前端检查，并验证构建产物中的配置注入。未执行线上部署或真实 hCaptcha/GitHub OAuth 联调。
