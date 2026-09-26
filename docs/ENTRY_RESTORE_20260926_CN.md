# 2026-09-26 创作台入口恢复与独立更新验收

## 已上线结果

原问题是拆分后的主站缺少入口，且独立服务调用 Sub2API 时丢失登录 IP / User-Agent。
创作台数据库和对象并未消失。现在主站侧边栏直接提供“创作台”，管理员还有“更新管理”。

| 组件 | 正式版本 | 入口 / 更新范围 |
| --- | --- | --- |
| Sub2API | `0.2.4-operator.4`，blue 活动 | 左侧更新卡；只切 Sub2API 蓝绿版本 |
| Canvas stable | `0.1.1-session.1` | `/studio/`；右侧更新卡，只更新 Canvas |
| Canvas candidate | `0.1.1-session.1`，只读 | `/studio-next/`；命令行预验收 |

登录后进入 https://dolorous.asia/studio/ 。如仍看到旧界面，强制刷新主站；此前因指纹
不匹配而失效的登录会话需重新登录。首次拆分不重做、不自动迁移或绑定 Key。

## 日常分别更新，一步一步

1. 在主站登录管理员，打开侧边栏“更新管理”。桌面左卡是 Sub2API，右卡是创作台。
2. 要升级哪部分就只操作对应卡；另一部分保留当前版本。两部分不能同时发布。
3. 若显示“已是最新版本”，说明当前 catalog 没有新候选。先按
   [更新中心手册](WEB_DEPLOYMENT_CENTER_CN.md) 合并、修改代码，运行 CI，构建不可变镜像，
   再用 `deploy/operator/update_catalog.py` 登记对应组件的新版本和 digest。
4. 点击该卡更新并确认，等待健康检查完成；系统会自动执行 `post-update-reconcile.sh`。
5. 查看任务是否 `succeeded`、对账是否成功。`attention_required` 是未绑定等差异提醒，
   不表示发布失败；它不会自动复制 Key。需要使用的 Key 在创作台内手动绑定一次。
6. 打开创作台确认原项目可以读取。异常时按手册回滚对应组件，不重建数据卷或主密钥。

主站仍需使用保留入口/部署中心补丁的自有镜像。直接换成不含这些补丁的上游镜像，
侧边栏入口可能再次消失；独立 `/studio/` 和数据仍由 Canvas 服务保留。
上游合并流程见更新中心手册第 9 节，不再把旧内嵌创作台代码合回主站。

## 发布与验证证据

- Sub2API 源码提交：`49be17f9b8644edfd2850b00957d105bc1a30447`。
- Sub2API 镜像：`ghcr.io/dolorous01/sub2api@sha256:0cec47d5358c364e1ceb583055cc438ef77768bb67eec12701293a86fd0952c5`。
- Canvas 源码提交：`29b0d860181061f3808f6476d7ed04a1fbb342d1`，标签 `v0.1.1-session.1`。
- Canvas API：`ghcr.io/dolorous01/canvas-standalone-api@sha256:ae4c1fcc0485079f31cf7921432e32bc050d3c5a315121e61aed96e52cd50af1`。
- Canvas Web：`ghcr.io/dolorous01/canvas-standalone-web@sha256:dfefd2dce3f9cf671e7075395cc6712ce235b523dd345154ab0260b1a8568378`。
- Sub2API 完整 CI `36226079432`、安全扫描 `36226079424`、镜像发布 `36226079421` 通过。
- Canvas 完整 CI `36227941006`、镜像发布 `36228053649` 通过；本地部署回归为
  21 项代理/控制器、3 项 catalog、10 项对账及三组 shell 检查。
- candidate 对账：`state/candidate/reconcile/reconcile-20260926T075659Z-4031810.json`。
- 正式 Canvas 更新任务：`deploy-af7ece822da24ebf84dd9c51ac454791`，发布和对账成功。
- 正式 Sub2API 更新任务：`deploy-dccf2b8933074c97acfa44d12137843a`，发布和对账成功。
- 任务状态与报告分别位于 `state/operator/operations/`、`state/operator/reports/`。
- Sub2API 保护证据：`state/official-guard/release-20260926T075822Z.DmrJzd/result.env`，
  `canvas_unchanged=true`，证明主站更新未重建创作台或改变其路由。
- 公共域名实际登录后，更新管理、Canvas session、Key candidates、projects 均 HTTP 200。
- 公共前端资源包含 `sidebar-canvas-studio` 与 `sidebar-deployment-center`；`/studio/` 和 readiness 均 200。

蓝绿观察期曾遇一次更新状态 503；切换后公共域名复验正常。本轮验证未进行收费生成，
也没有浏览器自动化截图。最后一次对账可见 8 个 Key、1 个可用、7 个未绑定；结果会随
用户操作变化，报告均为 `plaintext_key_accessed=false`、`mutation_performed=false`。

## 旧代码清理与恢复

- `/home/ubuntu/sub2api-canvas-mvp`：干净历史工作树已移除（约 46MB），
  `feat/infinite-canvas-mvp` 分支保留，可用 `git worktree add` 恢复源代码；生成资源需重建。
- 旧内嵌实现移至 `/home/ubuntu/legacy-code-archive-20260926/sub2api-infinite-canvas`。
  `.gitignore`、`.claude/` 和迁移手册等未提交修改全部保留；仅移除约 941MB 的
  `frontend/node_modules`，需要时根据锁文件重新安装依赖。
- 旧代理副本移至 `/home/ubuntu/legacy-code-archive-20260926/sub2api-path-proxy-work`。
- 归档目录权限 0700，约 59MB；运行服务没有引用这些历史目录。
- `/home/ubuntu/sub2api` 主工作树及其他分支工作树有用户改动，保留不动。
- 数据库、对象、主密钥、final 迁移备份、旧源数据及冻结标记均保留。
- 旧内嵌镜像保留标签 `sub2api-legacy-canvas:retain-until-20261013`，未执行全局 Docker prune。

需要恢复归档工作树时先确认目标路径不存在，然后执行：

```bash
git -C /home/ubuntu/sub2api worktree move /home/ubuntu/legacy-code-archive-20260926/sub2api-infinite-canvas /home/ubuntu/sub2api-infinite-canvas
git -C /home/ubuntu/sub2api worktree add /home/ubuntu/sub2api-canvas-mvp feat/infinite-canvas-mvp
```

恢复旧源代码不等于恢复线上旧部署；不要覆盖当前运行文件或解冻旧 Canvas 写入。
会话兼容的网络配置、临时凭据处理见 [SESSION_BINDING_CN.md](SESSION_BINDING_CN.md)。
