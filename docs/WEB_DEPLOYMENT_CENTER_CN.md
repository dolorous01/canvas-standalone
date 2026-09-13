# Sub2API / 创作台网页独立更新中心

状态：代码已实现并通过单元测试；正式代理与正式镜像尚未切换  
更新日期：2026-09-13

## 1. 为什么当前前端仍然不能更新

当前失败不是偶发网络问题，而是旧调用链已不适合拆分部署：

~~~text
旧前端“立即更新”
  -> POST /api/v1/admin/system/update
  -> 路径代理返回 403 immutable_deployment_required
~~~

这个 403 必须保留。旧接口只会在 Sub2API 内部原地替换程序，不知道独立创作台、蓝绿
发布、Canvas route 或更新后对账。解除 403 后，下一次更新仍可能把镜像内的创作台替换掉。

现在还有三个启用条件未完成：

1. 正式 Sub2API 仍运行旧前端镜像，新双卡片前端尚未发布；
2. 正式代理仍运行 /home/ubuntu/ResearchWang13/scripts 下的旧副本，没有加载
   deployment_operator.py 和 operator 参数；
3. 独立 Canvas stable 尚未接管 /studio，控制器会主动返回 canvas_stable_required。

正确的新调用链是：

~~~text
管理员双卡片界面
  -> /api/v1/admin/deployments/*
  -> 路径代理实时验证管理员 Bearer
  -> 从本机 catalog 读取已批准的不可变 digest
  -> 只运行所选组件的受控发布脚本
  -> 健康检查和另一组件不变检查
  -> post-update-reconcile.sh
  -> 页面轮询任务与脱敏对账结果
~~~

## 2. 最终界面和更新边界

桌面弹窗固定两列，窄屏自动变成一列：

| 左卡：Sub2API | 右卡：创作台 |
| --- | --- |
| 当前版本、批准版本、蓝绿状态 | 当前版本、批准版本、stable 状态 |
| 只更新/回滚 Sub2API | 只更新/回滚 Canvas stable |
| 不重建任何 canvas_* 容器 | 不切换 sub2api_blue/green |

两个卡片可分别操作，但不能同时发布。所有正式发布共享
/home/ubuntu/canvas-standalone/state/deployments.lock。

这里的“数据与 Key 同步”是安全的更新后对账：

- 两边继续使用各自原有 PostgreSQL、Redis、对象目录和密钥文件；
- 不创建新 volume，不复制整库，不迁移 Key 明文；
- 发布健康后读取脱敏 Key 元数据和绑定状态；
- 报告必须保持 plaintext_key_accessed=false 和 mutation_performed=false；
- 第一次正式拆分仍不迁移旧 Key，管理员开写后手动绑定一次。

## 3. 已实现的文件

最新版 Sub2API 独立工作树：

~~~text
/home/ubuntu/sub2api-latest-canvas
~~~

前端接入文件：

~~~text
frontend/src/api/admin/deployments.ts
frontend/src/components/common/DeploymentCenterDialog.vue
frontend/src/components/common/DeploymentOperationStatus.vue
frontend/src/components/common/VersionBadge.vue
frontend/src/i18n/locales/zh/misc.ts
frontend/src/i18n/locales/en/misc.ts
~~~

独立发布控制器：

~~~text
/home/ubuntu/canvas-standalone/deploy/proxy/deployment_operator.py
/home/ubuntu/canvas-standalone/deploy/proxy/sub2api_path_proxy.py
/home/ubuntu/canvas-standalone/deploy/operator/update_catalog.py
~~~

前端使用六个窄接口：

~~~text
GET  /api/v1/admin/deployments
POST /api/v1/admin/deployments/sub2api/update
POST /api/v1/admin/deployments/sub2api/rollback
POST /api/v1/admin/deployments/canvas/update
POST /api/v1/admin/deployments/canvas/rollback
GET  /api/v1/admin/deployments/operations/:id
~~~

## 4. 第一次启用

旧网页本身没有双卡片，所以第一次必须从终端 bootstrap。之后日常更新才从网页执行。

### 4.1 前置条件

先完成 [UPGRADE_CN.md](UPGRADE_CN.md) 第 8 节，并确认：

1. 正式 Sub2API 与旧创作台数据已有备份；
2. 独立 Canvas stable 已接管 /studio 和 /canvas-api；
3. canvas-verify.sh --slot stable 通过；
4. 管理员已在新创作台手动绑定自己的低额度 Key；
5. 当前用户能运行 Docker，并能无交互调用 root-owned 蓝绿脚本；
6. 磁盘、内存和数据库迁移门禁通过。

stable 未接管前可以安装控制器并看状态，但更新按钮会保持禁用。不要绕过
canvas_stable_required。

### 4.2 准备最新版 Sub2API 工作树

当前机器已创建：

~~~text
路径：/home/ubuntu/sub2api-latest-canvas
分支：feat/independent-deployment-ui-latest
基线：origin/main
~~~

其他机器首次创建时执行：

~~~bash
git -C /home/ubuntu/sub2api fetch --prune origin
git -C /home/ubuntu/sub2api worktree add \
  -b feat/independent-deployment-ui-latest \
  /home/ubuntu/sub2api-latest-canvas \
  origin/main
~~~

不要在 /home/ubuntu/sub2api 的旧脏工作树上直接 pull。

### 4.3 验证前端补丁

~~~bash
cd /home/ubuntu/sub2api-latest-canvas/frontend
pnpm install --frozen-lockfile
pnpm run lint:check
pnpm exec vitest run \
  src/api/admin/__tests__/deployments.spec.ts \
  src/components/common/__tests__/DeploymentCenterDialog.spec.ts \
  src/components/common/__tests__/VersionBadge.deployment.spec.ts
pnpm run check:i18n
NODE_OPTIONS=--max-old-space-size=3072 pnpm run typecheck
NODE_OPTIONS=--max-old-space-size=3072 pnpm exec vue-tsc -b --pretty false
NODE_OPTIONS=--max-old-space-size=3072 pnpm exec vite build
~~~

当前正式机只有约 2 GiB 内存且负载较高，完整 Vite 转换可能长时间换页。推荐在至少
4 GiB 可用内存的 CI/构建机生成镜像；不要因机器慢而跳过类型检查。

### 4.4 构建带更新中心的自有 Sub2API 镜像

不能把官方原版镜像直接登记到左卡。官方镜像不含本更新中心，发布后创作台不会消失，
但双卡片入口会从 Sub2API 前端消失。

~~~bash
integration=/home/ubuntu/sub2api-latest-canvas
base_version=$(tr -d '\r\n' < "$integration/backend/cmd/server/VERSION")
short_commit=$(git -C "$integration" rev-parse --short=9 HEAD)
version="${base_version}-operator.${short_commit}"
commit=$(git -C "$integration" rev-parse HEAD)
image_tag=ghcr.io/替换为你的组织/sub2api-operator:"$version"

docker buildx build \
  --platform linux/amd64 \
  --build-arg VERSION="$version" \
  --build-arg COMMIT="$commit" \
  --tag "$image_tag" \
  --push \
  "$integration"

docker buildx imagetools inspect "$image_tag"
~~~

记录顶层 sha256 摘要。catalog 只能使用
ghcr.io/你的组织/sub2api-operator@sha256:完整摘要，不能使用 tag 或 latest。

### 4.5 安装代理控制器文件

此步骤先复制文件，不重启正式代理：

~~~bash
standalone_root=/home/ubuntu/canvas-standalone
runtime_scripts=/home/ubuntu/ResearchWang13/scripts
operator_state="$standalone_root/state/operator"
catalog="$operator_state/releases.json"

install -m 755 \
  "$standalone_root/deploy/proxy/sub2api_path_proxy.py" \
  "$runtime_scripts/sub2api_path_proxy.py"
install -m 644 \
  "$standalone_root/deploy/proxy/deployment_operator.py" \
  "$runtime_scripts/deployment_operator.py"
install -d -m 700 "$operator_state"
~~~

安装前另存当前正式代理和 user service；不要删除已有 .bak-* 文件。

### 4.6 登记批准版本

登记左卡：

~~~bash
"$standalone_root/deploy/operator/update_catalog.py" \
  --catalog "$catalog" \
  sub2api \
  --version "$version" \
  --image 'ghcr.io/你的组织/sub2api-operator@sha256:完整摘要' \
  --release-url 'https://你的发布记录地址'
~~~

Canvas candidate 验收后登记右卡：

~~~bash
"$standalone_root/deploy/operator/update_catalog.py" \
  --catalog "$catalog" \
  canvas \
  --version 'Canvas版本' \
  --build-id '同一次构建ID' \
  --api-image 'ghcr.io/你的组织/canvas-api@sha256:完整摘要' \
  --web-image 'ghcr.io/你的组织/canvas-web@sha256:完整摘要' \
  --release-url 'https://你的发布记录地址'

"$standalone_root/deploy/operator/update_catalog.py" \
  --catalog "$catalog" \
  validate
stat -c '%a %U:%G %n' "$catalog"
~~~

预期权限为 600。工具每次只修改指定组件并保留另一张卡目标；无效 tag 不会覆盖旧文件。

### 4.7 在备用端口验证

当前机器的 `18080`、`18081` 是 Sub2API 蓝绿后端端口，不要占用。先确认 `18082`
没有监听者；输出应只有标题行，否则换一个未使用的本机端口，并同步替换本节全部 `18082`：

~~~bash
ss -ltn 'sport = :18082'
~~~

终端 A：

~~~bash
"$runtime_scripts/sub2api_path_proxy.py" \
  --host 127.0.0.1 \
  --port 18082 \
  --sub2api http://127.0.0.1:18080 \
  --auto-clean http://127.0.0.1:8093 \
  --canvas-routes-file "$standalone_root/state/proxy-routes.rehearsal.json" \
  --deployment-catalog "$catalog" \
  --deployment-state-dir "$operator_state" \
  --deployment-dir "$standalone_root/deploy" \
  --deployment-timeout-seconds 3600 \
  --log-file "$operator_state/proxy-smoke.log"
~~~

终端 B：

~~~bash
curl -sS -o /tmp/operator-no-auth.json -w '%{http_code}\n' \
  http://127.0.0.1:18082/api/v1/admin/deployments
curl -sS -o /tmp/operator-old-update.json -w '%{http_code}\n' \
  -X POST http://127.0.0.1:18082/api/v1/admin/system/update
jq . /tmp/operator-no-auth.json /tmp/operator-old-update.json
~~~

预期依次为 401 invalid_admin_bearer 和 403 immutable_deployment_required。

再只读查询管理员状态：

~~~bash
read -r -s -p '粘贴管理员 auth_token（输入不可见）: ' admin_token
printf '\n'
curl -fsS \
  -H "Authorization: Bearer $admin_token" \
  http://127.0.0.1:18082/api/v1/admin/deployments |
  jq '{components, active_operation, reconciliation}'
unset admin_token
~~~

验证后在终端 A 按 Ctrl+C 停止，不要让两个代理监听正式 8080。

### 4.8 修改正式 user service

执行下面的命令创建 drop-in：

~~~bash
systemctl --user edit sub2api-path-proxy.service
~~~

填入完整覆盖内容：

~~~ini
[Service]
ExecStart=
ExecStart=/home/ubuntu/ResearchWang13/scripts/sub2api_path_proxy.py --host 0.0.0.0 --port 8080 --sub2api http://127.0.0.1:18080 --auto-clean http://127.0.0.1:8093 --canvas-routes-file /home/ubuntu/canvas-standalone/state/proxy-routes.rehearsal.json --canvas-source-maintenance-file /home/ubuntu/canvas-standalone/state/operator/legacy-canvas-frozen --deployment-catalog /home/ubuntu/canvas-standalone/state/operator/releases.json --deployment-state-dir /home/ubuntu/canvas-standalone/state/operator --deployment-dir /home/ubuntu/canvas-standalone/deploy --deployment-timeout-seconds 3600 --log-file /home/ubuntu/ResearchWang13/data/logs/path_proxy.log
~~~

然后执行：

~~~bash
systemctl --user daemon-reload
systemctl --user restart sub2api-path-proxy.service
systemctl --user status sub2api-path-proxy.service --no-pager
journalctl --user -u sub2api-path-proxy.service -n 100 --no-pager
~~~

启动失败时恢复代理和 service 备份，不要修改 HAProxy、数据库或 route 掩盖错误。

### 4.9 第一次发布带双卡片的 Sub2API

只有 4.1 全部完成后执行：

~~~bash
bearer_file="$operator_state/bootstrap-auth-token"
report="$operator_state/bootstrap-reconcile.json"

umask 077
read -r -s -p '粘贴管理员 auth_token（输入不可见）: ' admin_token
printf '\n'
printf '%s\n' "$admin_token" >"$bearer_file"
unset admin_token
chmod 600 "$bearer_file"

cd "$standalone_root"
./deploy/sub2api-release.sh \
  --image 'ghcr.io/你的组织/sub2api-operator@sha256:完整摘要' \
  --route-file "$standalone_root/state/proxy-routes.rehearsal.json" \
  --reconcile-bearer-file "$bearer_file" \
  --reconcile-report "$report"

rm -f -- "$bearer_file"
jq '{status, keys, plaintext_key_accessed, mutation_performed}' "$report"
~~~

预期新 Sub2API 健康、Canvas stable 身份完全不变，报告为 ok 或 attention_required，两个
安全字段均为 false。此后版本菜单会出现“更新管理”。

## 5. 日常更新

### 5.1 只更新 Sub2API

1. 集成分支获取最新上游提交并解决小范围前端冲突。
2. 运行 4.3 的所有检查。
3. 构建并推送新 sub2api-operator 镜像。
4. 从 registry 取得不可变 digest。
5. 用 update_catalog.py 的 sub2api 子命令更新左卡批准版本。
6. 打开“更新管理”，在左卡点击更新并二次确认。
7. 等任务显示发布成功和数据/Key 对账结果。
8. 验证 blue/green、公共健康检查和 /studio。

Canvas 容器、数据库、对象目录和 route hash 必须不变。

### 5.2 只更新创作台

1. Canvas CI 生成同一次提交的 API/Web 两个 digest。
2. 先按 [UPGRADE_CN.md](UPGRADE_CN.md) 第 5 节更新 candidate。
3. 在 /studio-next 完成人工验收。
4. 用 update_catalog.py 的 canvas 子命令登记两个 digest。
5. 在右卡点击更新并二次确认。
6. 等任务显示 stable 健康和数据/Key 对账结果。
7. 验证 /studio、/canvas-api、JS Content-Type 和随机项目。

Sub2API 活动颜色与官方数据库 schema 必须不变。

### 5.3 自动对账顺序

控制器会：

1. 把当前管理员 Bearer 写入一次性 0600 文件；
2. 固定确认时 catalog 中的 digest；
3. 调用对应 release 脚本；
4. 健康后调用 post-update-reconcile.sh；
5. 把脱敏报告写入 state/operator/reports；
6. 立即删除一次性 Bearer；
7. 只向页面返回状态、摘要和计数。

缺少报告时任务以 reconciliation_report_missing 失败；违反只读或无明文约束时以
reconciliation_failed 失败。对账失败不会自动回滚已健康的版本。

## 6. 更新后验证

页面确认组件、目标版本、任务 succeeded 和对账结果。服务器再执行：

~~~bash
curl -fsS http://127.0.0.1:8080/health | jq .
cd /home/ubuntu/canvas-standalone
./deploy/canvas-verify.sh \
  --slot stable \
  --route-file /home/ubuntu/canvas-standalone/state/proxy-routes.rehearsal.json

find state/operator/tokens -mindepth 1 -maxdepth 1 -type f -print
find state/operator/operations -maxdepth 1 -type f -printf '%TY-%Tm-%Td %TH:%TM:%TS %p\n'
find state/operator/reports -maxdepth 1 -type f -printf '%TY-%Tm-%Td %TH:%TM:%TS %p\n'
~~~

任务结束后 tokens 必须为空。操作、日志和报告应为 0600，目录应为 0700。

## 7. 分别回滚

- 左卡回滚只切回上一个 Sub2API 颜色；
- 右卡回滚只恢复上一组 Canvas stable 镜像与 build ID；
- 回滚不恢复数据库，也不执行 Key 对账；
- 网页不可用时使用 UPGRADE_CN.md 第 5.8 或 6.6 节的 CLI。

不要因为一边故障而同时回滚另一边。

## 8. 常见错误

| 结果 | 原因 | 处理 |
| --- | --- | --- |
| 403 immutable_deployment_required | 仍调用旧接口 | 保留门禁，确认新前端已发布 |
| deployment_operator_disabled | 正式代理缺 operator 参数 | 核对 4.5 至 4.8 |
| canvas_stable_required | stable 尚未接管 /studio | 完成首次切流，不能绕过 |
| release_not_approved | catalog 没有新 digest | 用 4.6 登记并验证 |
| deployment_in_progress | 另一张卡正在发布 | 等待任务终态 |
| reconciliation_failed | token、契约或安全检查失败 | 查看脱敏报告后决定单边回滚 |
| 双卡片入口更新后消失 | 发布了官方原版镜像 | 从自有集成分支重新构建 |

## 9. 以后跟随上游

建议把集成分支提交到受控 fork。只有工作树干净且补丁已提交时才 rebase：

~~~bash
git -C /home/ubuntu/sub2api fetch --prune origin
git -C /home/ubuntu/sub2api-latest-canvas rebase origin/main

cd /home/ubuntu/sub2api-latest-canvas/frontend
pnpm install --frozen-lockfile
pnpm run lint:check
pnpm run check:i18n
NODE_OPTIONS=--max-old-space-size=3072 pnpm run typecheck
pnpm exec vitest run \
  src/api/admin/__tests__/deployments.spec.ts \
  src/components/common/__tests__/DeploymentCenterDialog.spec.ts \
  src/components/common/__tests__/VersionBadge.deployment.spec.ts
~~~

冲突通常只在 VersionBadge.vue 和两个 misc.ts。每次构建使用新版本名和 digest，经 catalog
批准后从左卡发布。创作台继续独立保存在 /home/ubuntu/canvas-standalone，不重新合入
Sub2API 仓库。

## 10. 本机验收记录

2026-09-13 已将集成工作树快进到 `origin/main@bdb42e22f`，本地更新中心补丁无冲突恢复。
正式服务未切换，验收均使用备用端口、假 catalog 或浏览器 mock：

- 备用代理 `127.0.0.1:18082`：新状态接口无令牌返回
  `401 invalid_admin_bearer`，伪令牌返回 `401 admin_access_required`；
- 同一备用代理：旧 `POST /api/v1/admin/system/update` 继续返回
  `403 immutable_deployment_required`；
- Playwright 桌面 `1365x900`：Sub2API 卡片在左、创作台卡片在右；
- Playwright 移动端 `390x844`：两张卡片改为纵向单列，页面横向溢出为 `0px`；
- 桌面和移动端均未出现部署中心运行时错误或控件重叠。
- `make test-deploy`：代理/控制器 17 项、catalog 3 项、对账 9 项及两组 Shell 守卫通过；
- 前端 deployment 定向测试 7 项、i18n 完整性测试 3 项、完整 ESLint 和
  `pnpm run typecheck` 通过。

本机在最新基线上的 `pnpm run typecheck` 已通过；构建模式 `vue-tsc -b` 和完整 Vite
生产构建仍受约 2 GiB 内存限制，曾在 swap 耗尽、`transforming` 或后台 checker 阶段
无法完成。生产镜像必须在至少 4 GiB 可用内存的 CI/构建机完成 4.3 的最后两条命令后
再发布；不能把开发服务器输出当作生产构建产物。
