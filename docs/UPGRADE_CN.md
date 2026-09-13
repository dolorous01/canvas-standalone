# Sub2API 与创作台双轨升级手册

状态：正式拆分已完成，Sub2API 与 Canvas 可独立更新
最近验证：2026-09-13

## 1. 最终目标

Sub2API 和创作台必须是两个独立发布单元：

```text
Cloudflare -> 路径代理 :8080
  /studio、/canvas-static -> Canvas stable Web :18100
  /canvas-api             -> Canvas stable API :18101
  /studio-next            -> Canvas candidate Web :18110
  /canvas-api-next        -> Canvas candidate API :18111
  其他路径                 -> HAProxy :18080 -> 官方 Sub2API blue/green
```

拆分完成后：

- 更新官方 Sub2API 只切换 `sub2api_blue`/`sub2api_green`，不重建 Canvas 容器；
- 更新创作台只重建 `canvas_stable_*` 或 `canvas_candidate_*`，不改官方数据库；
- 两边都使用不可变镜像 digest，可分别回滚；
- `/api/v1/admin/system/update` 等应用内原地更新入口保持 403，更新统一走部署脚本。

Key 和数据的长期边界、首次不迁移 Key、后续更新自动对账的逐步操作见
[KEY_RECONCILIATION_CN.md](KEY_RECONCILIATION_CN.md)。

网页左右双卡片的首次安装、最新版自有镜像构建、catalog、systemd、日常独立更新和
验证步骤见 [WEB_DEPLOYMENT_CENTER_CN.md](WEB_DEPLOYMENT_CENTER_CN.md)。

## 2. 当前真实状态

截至 2026-09-13 正式切流完成后：

| 项目 | 当前状态 |
| --- | --- |
| 正式 `/studio` | 独立 Canvas stable `0.1.0`，已接管并允许写入 |
| stable API/Web 端口 | `127.0.0.1:18101` / `127.0.0.1:18100` |
| stable 镜像 | API `sha256:a4a1ce8e...b6b1a90e`；Web `sha256:c5ffa3c8...d3071e` |
| 独立 candidate | `0.1.0-rehearsal.5`，入口 `/studio-next`，只读对账接口已部署 |
| candidate API/Web 端口 | `127.0.0.1:18111` / `127.0.0.1:18110` |
| candidate 写入 | 关闭，`CANVAS_WRITES_ENABLED=false` |
| candidate allowlist | 仅正式管理员 `external_user_id=1` |
| 路由文件 | `/home/ubuntu/canvas-standalone/state/proxy-routes.rehearsal.json` |
| 迁移摘要 | `74565d67189ec023533ec5538a250228a53954cb57ae75963cf50fb6e4cd6813` |
| 当前 Sub2API | `0.2.4-operator.1`，green 活动，镜像 `sha256:166fdf47...0960c205` |
| 官方更新保护 | 已启用；发布前后强制证明 Canvas stable 身份完全不变 |
| 旧 Canvas 写入 | 永久冻结；`state/operator/legacy-canvas-frozen` 不得删除 |
| 首次 Key 方案 | 已按约定迁移 0 个 credential；管理员按需手动绑定一次 |
| 后续 Key 对账 | 网页独立更新后自动运行，只读且不接触 Key 明文 |
| Canvas 到 Sub2API | 本机桥接流量被防火墙丢弃，两个 slot 固定使用 `https://dolorous.asia` |
| 网页更新控制器 | 已安装；版本菜单中的“更新管理”为左右双卡片入口 |

现在可以分别更新两边。更新 Sub2API 只切 blue/green，更新 Canvas 先发布 candidate，验收
后再发布 stable。旧的 `/api/v1/admin/system/update` 继续返回 403，这是预期保护；不要
为了恢复旧按钮而解除它。

### 2.1 2026-09-13 首次切流结果

1. 用户明确接受本次先提供图片能力；视频和音频生成未作为已支持能力发布。
2. 旧 Canvas 写入在 `2026-09-13T03:43:22Z` 冻结，GET/HEAD 继续可读。
3. final dump、对象归档和 export 已保存到权限 `0700` 的切流证据目录。
4. 21 个项目、15 个资产和 20 个对象通过 `verify-transfer`；credential 数为 0。
5. `/studio` 已切到 stable，管理员身份、4 个 active 项目和随机 3 个项目通过只读抽查。
6. stable 开写后完成创建、保存、读取和删除临时项目的 smoke。
7. Sub2API 蓝绿更新到 `0.2.4-operator.1`，保护脚本证明 Canvas 容器和 route 未改变。

完整证据索引和已知偏差见 [PRODUCTION_CUTOVER_20260913_CN.md](PRODUCTION_CUTOVER_20260913_CN.md)。

## 3. 每次操作先设置路径

下面都是非敏感变量，可以在新终端中设置：

```bash
standalone_root=/home/ubuntu/canvas-standalone
canvas_route_file=/home/ubuntu/canvas-standalone/state/proxy-routes.rehearsal.json
sub2_ops=/home/ubuntu/ResearchWang13/blue-green
```

不要把 `.env`、数据库 URL、API Key、JWT secret 或 Canvas 加密主密钥放进变量输出、
文档、Git 或聊天记录。

## 4. 不可违反的规则

1. 不执行 `docker compose down` 或 `docker compose down -v`。
2. 不删除 Canvas/PostgreSQL volume，也不把数据库恢复当普通代码回滚。
3. 正式镜像只使用 `repository@sha256:<64 hex>`，不使用 `latest`。
4. 正式环境不使用 `--allow-local-image`；它只供本机演练。
5. 更新 Sub2API 时不编辑 Canvas route、数据库、对象目录和 release state。
6. 更新 Canvas 时不编辑 Sub2API `versions.env`、数据库 schema 或 blue/green 颜色。
7. 一个验证命令失败就停止，不继续切 stable。
8. Canvas 第一次接收正式写入后，不再把写流量切回旧内嵌 Canvas。
9. 所有正式发布共享 `state/deployments.lock`；不要同时运行 Canvas 和 Sub2API 更新。
10. 普通更新不复制数据或 Key；继续使用各自原有持久化存储，更新后只运行脱敏只读对账。
11. 首次切流期间，旧创作台写入必须由代理维护标记冻结；不能只靠口头通知或关闭页面。

## 5. 独立更新创作台

本节是拆分完成后的常规 Canvas 更新流程。先 candidate，人工验收后再 stable。

### 5.1 取得不可变镜像

从 Canvas CI 记录同一次构建产出的两个 digest：

```bash
canvas_web_image='ghcr.io/OWNER/canvas-standalone-web@sha256:替换为64位摘要'
canvas_api_image='ghcr.io/OWNER/canvas-standalone-api@sha256:替换为64位摘要'
```

Web 和 API 的版本标签、源码提交和 CI 运行必须对应。不要把普通 tag 当成最终部署输入。

### 5.2 检查更新前状态

```bash
cd "$standalone_root"
./deploy/canvas-status.sh --slot candidate
./deploy/canvas-verify.sh \
  --slot candidate \
  --route-file "$canvas_route_file"
```

预期：容器全部 `healthy`，`canvas-verify.sh` 输出 `verification passed`。如果当前
candidate 本来就坏了，先查明原因，不用新版本覆盖证据。

### 5.3 candidate dry-run

```bash
cd "$standalone_root"
./deploy/canvas-release.sh \
  --slot candidate \
  --web-image "$canvas_web_image" \
  --api-image "$canvas_api_image" \
  --route-file "$canvas_route_file" \
  --dry-run
```

dry-run 会检查 digest、镜像标签、Compose 配置和待写入的路由，不启动或替换容器。

### 5.4 发布 candidate

```bash
cd "$standalone_root"
./deploy/canvas-release.sh \
  --slot candidate \
  --web-image "$canvas_web_image" \
  --api-image "$canvas_api_image" \
  --route-file "$canvas_route_file" \
  --reconcile-bearer-file "$bearer_file"
```

脚本固定执行以下顺序：数据库健康、schema migration、强制重建 API/Worker/Web、连续
ready、镜像 ID 核对、原子更新 route build ID、公共入口验证、写 release state 和审计。
任何门禁失败会恢复旧容器、旧 release state 和旧路由。schema migration 是前向的，
不会自动降级。`bearer_file` 按 Key 对账手册第 4 节准备；首次拆分尚未绑定 Key 时省略
这个参数。后续更新时，对账在健康发布完成后执行，对账失败不会自动回滚健康版本。

### 5.5 candidate 机器验证

```bash
cd "$standalone_root"
./deploy/canvas-verify.sh \
  --slot candidate \
  --route-file "$canvas_route_file"
./deploy/tests/smoke-proxy.sh "$canvas_route_file"
```

再明确检查 JavaScript 不是官方 SPA 的 HTML 兜底：

```bash
build_id="$(jq -r '.candidate.build_id' "$canvas_route_file")"
html="$(curl -fsS http://127.0.0.1:8080/studio-next)"
asset_path="$(sed -n 's#.*src="\([^\"]*canvas-[^\"]*\.js\)".*#\1#p' <<<"$html" | head -1)"
printf 'build_id=%s\nasset=%s\n' "$build_id" "$asset_path"
curl -fsSI "http://127.0.0.1:8080${asset_path}" | sed -n '1,20p'
```

预期 `Content-Type` 是 `application/javascript`，不是 `text/html`。只看到 HTTP 200
不算通过。

### 5.6 candidate 人工验证

用正式域名加 `/studio-next` 打开页面，并使用现有 Sub2API 管理员登录态。当前预览为
只读，预期管理员能看到自己迁移后的 4 个活动项目；保存、绑定 Key 等 POST 会返回
`writes_disabled`，这是预期保护。

逐项确认：

- 项目列表、名称、更新时间和画布内容；
- 至少 3 个项目的节点、连线、视口和图片；
- 素材预览和原图下载；
- `/studio` 仍是原正式创作台；
- 普通用户访问 candidate API 得到 403；
- 登出或失效登录态得到 401。

### 5.7 发布 stable

只有首次切流已完成、candidate 人工验收通过并且 stable 数据库已是正式数据权威时，
才执行：

```bash
cd "$standalone_root"
./deploy/canvas-release.sh \
  --slot stable \
  --web-image "$canvas_web_image" \
  --api-image "$canvas_api_image" \
  --route-file "$canvas_route_file" \
  --dry-run

./deploy/canvas-release.sh \
  --slot stable \
  --web-image "$canvas_web_image" \
  --api-image "$canvas_api_image" \
  --route-file "$canvas_route_file" \
  --reconcile-bearer-file "$bearer_file"

./deploy/canvas-verify.sh \
  --slot stable \
  --route-file "$canvas_route_file"
```

stable 发布前脚本会自动做数据库备份。仍应在变更单中记录备份路径、两个镜像 digest、
旧版本和新版本。

### 5.8 Canvas 回滚

先 dry-run：

```bash
cd "$standalone_root"
./deploy/canvas-rollback.sh \
  --slot stable \
  --route-file "$canvas_route_file" \
  --dry-run
```

确认目标后执行：

```bash
./deploy/canvas-rollback.sh \
  --slot stable \
  --route-file "$canvas_route_file"

./deploy/canvas-verify.sh \
  --slot stable \
  --route-file "$canvas_route_file"
```

这只回滚 Canvas 镜像和 route build ID，不回滚数据库。只有明确的数据损坏处置流程才
允许数据库恢复。

## 6. 独立更新官方 Sub2API

本节只在独立 Canvas stable 已接管 `/studio` 后使用。Sub2API 使用现有 HAProxy
blue/green 发布，不再点击后台“一键更新”。必须从本仓库的受保护入口启动；它会调用
现有蓝绿脚本，但会在前后验证 Canvas stable 完全未变。

### 6.1 检查更新前 Canvas 身份

```bash
cd "$standalone_root"
./deploy/canvas-verify.sh \
  --slot stable \
  --route-file "$canvas_route_file"
sha256sum "$canvas_route_file"
docker inspect canvas_stable_api canvas_stable_worker canvas_stable_web \
  --format '{{.Name}} {{.Image}}'
```

保存 route hash 和三个镜像 ID。它们在整个 Sub2API 更新过程中必须不变。

受保护入口还会记录 PostgreSQL/API/Worker/Web 的容器 ID、启动时间、重启次数、route
hash 和 release state hash。证据保存在 `state/official-guard/`，不包含 secret。

### 6.2 检查 Sub2API 当前状态

```bash
cd "$sub2_ops"
sudo ./status.sh
```

必须确认：活动颜色健康、HAProxy 与 state 对齐、`:18080` 和公共 `:8080/health`
健康、PostgreSQL/Redis 正常、磁盘和内存满足门禁。

### 6.3 选择带更新中心的集成镜像并 dry-run

把最新上游 Sub2API 合入更新中心补丁，通过 CI 后取得自有镜像完整 digest。不要直接发布
不含双卡片前端的上游原版镜像，否则更新中心入口会消失。

```bash
# 这是首次正式发布值；以后替换成新集成构建的 digest。
sub2_image='ghcr.io/dolorous01/sub2api@sha256:166fdf4724b6c3885e243fd9c23f23d98eb4b83f2a437f9040e0c3160960c205'
docker buildx imagetools inspect "$sub2_image"

cd "$standalone_root"
./deploy/sub2api-release.sh \
  --image "$sub2_image" \
  --route-file "$canvas_route_file" \
  --dry-run
```

`imagetools inspect` 显示的顶层 `Digest` 必须与 `sub2_image` 中的 64 位摘要相同；不同
就停止并重新审核新版本。还要检查镜像标签中的源码 revision 和 operator 版本。核对
dry-run 显示的旧活动颜色、新颜色、目标镜像和迁移/备份计划。脚本只在调用
root-owned 蓝绿脚本时使用 `sudo`；不要把整个用户可写仓库交给 `sudo` 执行。

如果输出 `Canvas stable preflight failed`，说明第 8 节尚未完成，官方更新会被正确阻止。
不要增加跳过参数。

### 6.4 正式更新 Sub2API

```bash
cd "$standalone_root"
./deploy/sub2api-release.sh \
  --image "$sub2_image" \
  --route-file "$canvas_route_file" \
  --reconcile-bearer-file "$bearer_file"
```

脚本会把新镜像部署到非活动颜色，健康后切 HAProxy，并观察、排空旧连接。它不应
重建任何 `canvas_*` 容器，也不应修改 Canvas route 文件。受保护入口会在整个操作期间
持有全局发布锁，并在蓝绿脚本结束后重新验证公共 `/studio`、API release、JavaScript
Content-Type 和 Canvas 容器身份；任一变化都会退出失败并打印证据目录。机器门禁通过后，
同一命令再执行只读 Key 对账；报告默认写入 `state/stable/reconcile/`。

### 6.5 更新后双重验证

```bash
cd "$sub2_ops"
sudo ./status.sh
curl -fsS http://127.0.0.1:18080/health | jq .
curl -fsS http://127.0.0.1:8080/health | jq .

cd "$standalone_root"
./deploy/canvas-verify.sh \
  --slot stable \
  --route-file "$canvas_route_file"
sha256sum "$canvas_route_file"
docker inspect canvas_stable_api canvas_stable_worker canvas_stable_web \
  --format '{{.Name}} {{.Image}}'
```

Canvas route hash 和镜像 ID 必须与 6.1 完全相同。再用浏览器确认 `/studio` 项目可见、
保存正常，并做一次受控图片生成和一次 official usage 对账。

### 6.6 只回滚 Sub2API

```bash
cd "$standalone_root"
./deploy/sub2api-rollback.sh \
  --route-file "$canvas_route_file" \
  --dry-run
./deploy/sub2api-rollback.sh \
  --route-file "$canvas_route_file"

cd "$sub2_ops"
sudo ./status.sh
```

回滚后再次运行 6.5 的 Canvas 验证。不要运行 `canvas-rollback.sh`，因为本次没有更新
Canvas。

## 7. 两条更新链的边界

| 操作 | 可以改变 | 必须保持不变 |
| --- | --- | --- |
| Sub2API 更新 | blue/green 镜像、活动颜色、官方 schema | Canvas 容器、Canvas DB、对象目录、route 文件 |
| Canvas candidate 更新 | candidate 镜像/schema/route candidate 项 | 正式 `/studio`、Canvas stable、Sub2API 颜色 |
| Canvas stable 更新 | stable 镜像/schema/route stable 项 | Sub2API 颜色和官方 schema |
| Sub2API 回滚 | 官方活动颜色 | 所有 Canvas 状态 |
| Canvas 回滚 | 指定 Canvas slot 镜像和 build route | 官方活动颜色 |

两个发布入口和 `canvas-route.sh` 会竞争同一个全局锁，因此可以分别更新，但不能并发
更新。这样 route hash 的前后比对不会被另一条合法维护操作干扰，也避免同时执行数据库
migration。

## 8. 首次正式拆分切流

本机已于 2026-09-13 完成本节。以下内容作为首次迁移审计记录和新机器实施模板保留，
不得在日常更新中重复执行。新机器首次迁移仍必须安排维护窗口并按
`/home/ubuntu/sub2api-infinite-canvas/docs/CANVAS_STANDALONE_MIGRATION_CN.md`
第 23 节执行。

切流前必须同时满足：

1. 再做一次冻结时的生产数据库和对象备份；
2. 停止旧 Canvas 写入并等待旧任务进入终态；
3. 用只读角色从 `/home/ubuntu/ResearchWang13/data/image-jobs` 做最终 export；
4. 导入全新 stable 数据库并复制对象；
5. `verify-transfer` 摘要、行数、document hash、对象 hash 全部一致，credential 数为 0；
6. 完成管理员登录和随机 3 个项目人工验收；
7. 决定视频/音频能力门禁，不能静默降级；
8. 明确批准开写后，才让 stable 接管 `/studio` 和 `/canvas-api`；
9. 管理员在新创作台手动绑定一次自己的低额度 Key，不批量迁移旧 Key；
10. 观察通过后，才按第 6 节更新官方 Sub2API。

正式开写前可以恢复旧 route。独立 stable 接收第一笔写入后，旧内嵌 Canvas 不再是
权威，后续只能回滚独立 Canvas 镜像，不能把写流量切回旧服务。

旧端冻结标记固定为：

```bash
operator_state=/home/ubuntu/canvas-standalone/state/operator
legacy_freeze_marker="$operator_state/legacy-canvas-frozen"
install -d -m 700 "$operator_state"
umask 077
touch "$legacy_freeze_marker"
```

正式代理必须带
`--canvas-source-maintenance-file "$legacy_freeze_marker"`。标记存在时，旧
`/api/v1/image-canvas` 和 `/api/v1/admin/image-canvas` 下的写方法返回
`503 canvas_source_maintenance`，GET/HEAD 保持可读。只在 stable 开写前的回退中删除标记；
stable 开写后永久保留。

只读 route 和数据核验通过、并取得最后批准后，用下面的受控脚本开写：

```bash
./deploy/canvas-writes.sh \
  --enable \
  --route-file "$canvas_route_file" \
  --operator-external-id 1
```

脚本会持有全局发布锁，原子修改 stable 配置，重建 API/worker，验证环境、容器、路由和
健康状态，并写入审计记录；任一步失败都会恢复原写入模式。需要在开写前退回只读时，把
`--enable` 改为 `--disable`。开写后不得借此脚本切回旧内嵌 Canvas。

## 9. 常见故障

### 9.1 JS 请求是 200，但页面空白

先看 Content-Type：

```bash
curl -fsSI "http://127.0.0.1:8080/canvas-static/BUILD_ID/assets/FILE.js"
```

如果是 `text/html`，route build ID 与 Web 镜像不一致。修复指定 slot：

```bash
cd "$standalone_root"
./deploy/canvas-route.sh \
  --slot candidate \
  --route-file "$canvas_route_file"
./deploy/canvas-verify.sh \
  --slot candidate \
  --route-file "$canvas_route_file"
```

正常发布必须走 `canvas-release.sh --route-file ...`，不要只运行 `docker compose up`。

### 9.2 `/studio-next` 看起来像官方后台

检查线上代理进程参数是否包含 `--canvas-routes-file`，并检查 route 文件的 candidate
项。备用端口先跑：

```bash
./deploy/tests/smoke-proxy.sh "$canvas_route_file"
```

未通过时不要重启正式代理。

### 9.3 candidate 返回 `writes_disabled`

当前预览刻意只读，这是正常状态。不要为了测试方便直接把 candidate 指向正式写库。

### 9.4 candidate 返回 403

当前只允许 `external_user_id=1`。确认使用管理员账号；不要清空 allowlist 来绕过验收。

### 9.5 Canvas 登录返回 `official_unavailable`

先从对应 API 容器验证官方入口：

```bash
docker exec canvas_stable_api \
  /usr/local/bin/canvas-healthcheck http://host.docker.internal:18080/health
docker exec canvas_stable_api \
  /usr/local/bin/canvas-healthcheck https://dolorous.asia/health
```

本机防火墙会丢弃第一条 bridge-to-host 请求，第二条 HTTPS 请求应成功。因此生产
`deploy/environments/stable.env` 和 `candidate.env` 都使用：

```text
CANVAS_OFFICIAL_BASE_URL=https://dolorous.asia
```

修改后只重建对应 slot 的 API/worker，并继续使用当前 release 文件：

```bash
docker compose --project-name canvas-stable \
  --env-file deploy/environments/stable.env \
  --env-file state/stable/releases/current.env \
  -f deploy/compose.yml up -d --force-recreate api worker
./deploy/canvas-verify.sh --slot stable --route-file "$canvas_route_file"
```

不要把公网 IP、Docker gateway IP 或临时容器 IP写死到配置。若以后调整主机防火墙并恢复
内部入口，必须先在 candidate 验证认证、图片请求和超时路径，再修改 stable。

### 9.6 更新失败后的状态

```bash
cd "$standalone_root"
./deploy/canvas-status.sh --slot candidate
./deploy/canvas-verify.sh --slot candidate --route-file "$canvas_route_file"
tail -50 state/candidate/releases/audit.jsonl
```

发布脚本会恢复旧容器、route 和 release state；数据库 migration 不会自动倒退。保留
失败日志和备份，不删除 volume。

## 10. 当前验收证据

正式切流证据位于：

```text
/home/ubuntu/canvas-standalone/state/cutover-20260913T030438Z/
/home/ubuntu/canvas-standalone/state/official-guard/release-20260913T041128Z.h1Tkn5/
```

目录包含 final dump、对象归档、export manifest、`verify-transfer`、只读项目抽查、写入
smoke 和发布前后 Canvas 身份；目录权限为 `0700`，不得提交 Git。

两次规范化导出及两套空目标验证位于：

```text
/home/ubuntu/canvas-standalone/state/rehearsal-20260912-1/export-normalized-a/
/home/ubuntu/canvas-standalone/state/rehearsal-20260912-1/export-normalized-b/
/home/ubuntu/canvas-standalone/state/rehearsal-20260912-1/verify-normalized-2.json
/home/ubuntu/canvas-standalone/state/rehearsal-20260912-1/verify-normalized-3.json
```

两套结果均为：21 个项目（7 active、14 deleted）、15 个资产、20 个对象、
13,103,339 bytes，`verified=true`，内容摘要完全相同。
