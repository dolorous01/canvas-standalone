# 2026-09-13 正式拆分切流记录

## 1. 结果

正式切流已经完成：

- `/studio`、`/canvas-static` 和 `/canvas-api` 由独立 Canvas stable `0.1.0` 提供；
- Sub2API 由独立 blue/green 链发布，当前活动颜色为 green，版本为
  `0.2.4-operator.1`；
- 页面“更新管理”使用左侧 Sub2API、右侧创作台两张卡片，两个组件分别发布；
- 旧 Canvas 写入永久冻结，读取和 final 备份保留至少 30 天；
- 首次迁移没有复制 Key 或 credential，图片生成前由管理员手动绑定一次低额度 Key；
- 本次明确只发布图片能力，视频和音频生成不在已支持范围内。

## 2. 当前生产标识

| 组件 | 当前值 |
| --- | --- |
| Sub2API | `0.2.4-operator.1` |
| Sub2API image | `ghcr.io/dolorous01/sub2api@sha256:166fdf4724b6c3885e243fd9c23f23d98eb4b83f2a437f9040e0c3160960c205` |
| Canvas stable | `0.1.0` / build `0.1.0` |
| Canvas API image | `ghcr.io/dolorous01/canvas-standalone-api@sha256:a4a1ce8ef5b980cf2b7852d51abce0c594332427759c01bc2913706236b1a90e` |
| Canvas Web image | `ghcr.io/dolorous01/canvas-standalone-web@sha256:c5ffa3c8b669033128fa5176bee962cac85865c00363224e7eb6e33e27d3071e` |
| stable 端口 | Web `18100`，API `18101`，PostgreSQL `15432` |
| candidate 端口 | Web `18110`，API `18111`，PostgreSQL `15433` |
| route 文件 | `/home/ubuntu/canvas-standalone/state/proxy-routes.rehearsal.json` |
| 冻结标记 | `/home/ubuntu/canvas-standalone/state/operator/legacy-canvas-frozen` |
| catalog | `/home/ubuntu/canvas-standalone/state/operator/releases.json` |

stable 和 candidate 的 `CANVAS_OFFICIAL_BASE_URL` 均为 `https://dolorous.asia`。当前主机
防火墙会丢弃容器到 `host.docker.internal:18080` 的请求，不能改回该值后只看健康端点；
必须实际验证带登录的 `/canvas-api[-next]/v1/session`。

## 3. 数据迁移结果

最终冻结时间为 `2026-09-13T03:43:22Z`。final export 和两次 rehearsal 的规范化内容
摘要一致：

| 检查项 | 结果 |
| --- | --- |
| 内容摘要 | `74565d67189ec023533ec5538a250228a53954cb57ae75963cf50fb6e4cd6813` |
| 项目 | 21，active 7，deleted 14 |
| 资产 | 15 |
| 对象 | 20 个，`13,103,339` bytes |
| 对象摘要 | `a95a586932e612b53f5d423f27bceb21b182afb00c9239f1e75d2dc5717c4754` |
| credential | 0 |
| 不可领取的旧媒体历史 | 5 |
| `verify-transfer` | `verified=true` |

开写后做了两次临时项目清理。第一次测试误用 `PUT`，API 正确返回 405，`finally` 已删除
临时项目；第二次使用正确的 `PATCH`，完成创建、更新到 version 2、读取和删除。因此目标
库会多出 2 条软删除 smoke 记录，这是权威切换后的预期审计痕迹；活动项目数量没有变化。

## 4. 证据和回滚材料

私有证据目录：

```text
/home/ubuntu/canvas-standalone/state/cutover-20260913T030438Z/
```

关键文件：

```text
sub2api.final.dump
sub2api.final.dump.sha256
image-jobs.final.tar.gz
image-jobs.final.tar.gz.sha256
source-audit.final.json
export-final/manifest.json
verify-transfer.final.json
readonly-route-verification.json
write-smoke.json
stable-release-audit.after-write-enable.jsonl
sub2api_path_proxy.before.py
sub2api-path-proxy.service.before
```

Sub2API 发布前后 Canvas 身份证据：

```text
/home/ubuntu/canvas-standalone/state/official-guard/release-20260913T041128Z.h1Tkn5/
```

这些目录包含数据库和用户项目数据，权限必须保持 `0700/0600`，不得提交 Git、上传聊天
或放入公共 CI artifact。

## 5. 开写脚本修复记录

第一次运行 `canvas-writes.sh --enable` 时，API/worker 已经以 `writes=true` 健康启动并通过
route 验证，但审计输出引用了未加载的 `CANVAS_RELEASE`，在最后一行退出。旧源仍保持冻结，
没有双写或回流。数据权威边界按 API 启动时间记录为 `2026-09-13T04:01:33Z`，并在确认
健康后补记 `recovered_after_audit_failure=true`。

脚本现在从 `state/stable/releases/current.env` 读取并校验 release/build ID；新增
`deploy/tests/test-canvas-writes.sh`，真实覆盖 enable、disable 和审计字段。日常操作仍必须
只运行该脚本，不能直接编辑 `CANVAS_WRITES_ENABLED`。

## 6. 还需管理员手工完成一次

首次 Key 同步按要求跳过。需要进行图片生成前：

1. 重新登录正式域名并进入 `/studio`。
2. 打开创作台 Key 设置。
3. 从自己的 active Key 中选择一条低额度 Key，确认名称和额度后手动绑定。
4. 做一次低成本图片生成或编辑。
5. 在 Sub2API 用量页确认只有预期的一笔使用记录。
6. 按 [KEY_RECONCILIATION_CN.md](KEY_RECONCILIATION_CN.md) 第 6 节运行只读对账。

不要自动选择 2026-09-13 抽查时看到的 8 条候选中的任何一条。Key 选择属于管理员业务
决定，不属于数据迁移。

## 7. 以后分别更新

### 7.1 更新 Sub2API

1. 在 `/home/ubuntu/sub2api-latest-canvas` 合并最新上游。
2. 保留双卡片补丁，完成前端测试、类型检查、生产构建和安全扫描。
3. 推送自有不可变镜像，记录完整 digest；不能直接改用不含更新中心的上游镜像。
4. 用 `deploy/operator/update_catalog.py ... sub2api` 只更新左卡批准版本。
5. 管理员打开“更新管理”，在左卡点击更新并二次确认。
6. 等待 blue/green、Canvas 不变检查和自动只读 Key 对账全部完成。
7. 验证 `/health`、当前版本、`/studio` 和 `state/official-guard/` 证据。

### 7.2 更新创作台

1. Canvas CI 生成同一次提交的 API/Web 两个不可变 digest。
2. 用 `canvas-release.sh --slot candidate` 更新 candidate。
3. 在 `/studio-next` 验证登录、项目、静态资源和新功能。
4. 用 `deploy/operator/update_catalog.py ... canvas` 登记两个 digest 和同一个 build ID。
5. 管理员打开“更新管理”，在右卡点击更新并二次确认。
6. 等待 stable 发布和自动只读 Key 对账完成。
7. 验证 `/studio`、`/canvas-api/health/ready`、随机项目和当前 release state。

两个流程共享 `state/deployments.lock`，不能并发。普通更新继续使用原数据库、对象目录和
credential 主密钥，不做数据库之间的“同步复制”。

## 8. 回滚边界

- Sub2API 更新失败：只回滚 blue/green 颜色，再证明 Canvas 身份未变。
- Canvas 镜像更新失败：只回滚到兼容当前独立数据库的上一组 Canvas 镜像。
- 从 `2026-09-13T04:01:33Z` 起，不得删除旧冻结标记，也不得把写流量切回内嵌 Canvas。
- 不执行 `docker compose down -v`，不删除 stable PostgreSQL volume、对象目录或主密钥。

完整命令和停止条件见 [UPGRADE_CN.md](UPGRADE_CN.md)；网页操作和 catalog 见
[WEB_DEPLOYMENT_CENTER_CN.md](WEB_DEPLOYMENT_CENTER_CN.md)。
