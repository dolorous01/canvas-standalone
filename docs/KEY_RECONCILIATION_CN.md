# Sub2API 与创作台数据及 Key 对账手册

状态：首次拆分不迁移 Key；后续更新执行只读对账
适用仓库：`/home/ubuntu/canvas-standalone`

## 1. 先说结论

可以把 Sub2API 和创作台分开更新，也可以在任一方更新后运行同一个脚本核对 Key。这里
必须区分两个概念：

- **数据持久化**：更新容器时继续挂载原来的 PostgreSQL、对象目录和 secret，所以项目、
  素材、任务及已绑定凭证不会因普通更新消失；
- **更新后对账**：脚本实时读取 Sub2API 的 Key 元数据和创作台的绑定状态，检查两边是否
  一致，但不复制、打印或保存 Key 明文。

脚本名称虽然是“对账”，承担的就是安全版本的“同步确认”。不能把它改成跨库复制 Key
明文的脚本。

## 2. 两边分别保存什么

| 数据 | 权威位置 | 更新时如何处理 |
| --- | --- | --- |
| 用户、登录、Key 状态、额度、用量、计费 | Sub2API PostgreSQL/Redis | Sub2API 蓝绿更新继续使用原数据 |
| 项目、素材、编辑器文档、Canvas 任务 | 创作台 PostgreSQL/对象目录 | Canvas 更新继续使用原 volume/目录 |
| 已绑定 Key | 创作台 PostgreSQL 中的 AES-GCM 密文 | Canvas 更新继续使用原 DB 和主密钥 |
| Key 名称、状态、额度、用量 | 每次请求从 Sub2API HTTP API 实时读取 | 不复制到另一套数据库 |

Canvas 不读取 Sub2API 数据库，也不共享 JWT secret。用户主动绑定时，Canvas 后端只读取
一次完整 Key，并立即使用 Canvas 主密钥加密。普通升级不得创建新的 stable 数据库
volume、对象目录或凭证主密钥。

每次对账只覆盖 access token 所属的一个用户。管理员 token 也不会越权批量读取其他用户
的绑定；多用户环境应让各用户在自己的会话中确认和重绑，不能用管理员脚本汇总 Key
明文。

## 3. 第一次拆分怎么做

本次首次拆分采用以下固定规则：

1. 项目、素材、文档和允许迁移的历史记录按迁移手册导入独立 stable。
2. 不导出、不导入旧 Key 明文，也不导入旧 credential binding。
3. 切换 `/studio` 后先保持只读，确认项目和素材完整。
4. 得到开写批准后，管理员进入创作台的 Key 设置，选择自己的 Sub2API Key，手动绑定
   一次。
5. 用低额度 Key 做一次受控生成，并在 Sub2API 用量页确认只有一笔记录。

首次正式切流不需要运行 Key 迁移脚本，也不要给任何脚本增加“复制全部 Key”的功能。
这次人工绑定成功后，后续 Canvas 更新会保留同一数据库和同一主密钥，不需要再次绑定。

### 3.1 本机首次切流结果

2026-09-13 已完成项目和对象迁移并开启 stable 写入。目标库中 `canvas_credentials=0`；
管理员当时可见 8 条脱敏 Key 候选，但按“第一次不需要同步”的批准没有自动选择或绑定
任何一条，也没有执行带费用的生成 smoke。

需要生成图片时，管理员进入 `/studio` 的 Key 设置，只选择自己准备使用的低额度 active
Key 并确认绑定。绑定后检查 Sub2API 用量，再按第 6 节单独运行一次只读对账。不要使用
切流期间的临时登录令牌；重新登录取得当时有效的短期 access token。

## 4. 每次更新前准备一次性登录令牌

对账使用当前操作员自己的短期 **access token**，不使用 refresh token。令牌只通过权限
`0600` 的文件读取，不进入命令行参数、shell history、日志或报告。

使用网页双卡片更新中心时，不需要手工执行本节。浏览器现有登录请求会把 access token
发送给路径代理；代理完成实时管理员鉴权后，在服务器创建一次性 `0600` 文件，发布和
对账结束后立即删除。下面的步骤只用于第一次 bootstrap 或网页不可用时的 CLI 维护。

网页更新中心的安装与验证见
[WEB_DEPLOYMENT_CENTER_CN.md](WEB_DEPLOYMENT_CENTER_CN.md)。

1. 在正式域名登录 Sub2API。
2. 打开浏览器开发者工具的 `Application`（或“应用”）页。
3. 在当前域名的 `Local Storage` 中找到 `auth_token`，只复制它的值。不要复制
   `refresh_token`。
4. 在服务器终端执行：

```bash
standalone_root=/home/ubuntu/canvas-standalone
operator_dir="$standalone_root/state/operator"
bearer_file="$operator_dir/reconcile-bearer"

install -d -m 700 "$operator_dir"
umask 077
read -r -s -p '粘贴 auth_token（输入不可见）: ' reconcile_bearer
printf '\n'
printf '%s\n' "$reconcile_bearer" >"$bearer_file"
unset reconcile_bearer
chmod 600 "$bearer_file"
stat -c '%a %U %n' "$bearer_file"
```

预期权限开头是 `600`，所有者是当前执行发布脚本的用户。令牌过期时重新登录并重建这个
文件，不要长期保存 access token。

## 5. 方案一：让更新命令自动对账

这是日常更新推荐方式。先完成第 4 节，然后把
`--reconcile-bearer-file "$bearer_file"` 加到真实发布命令。对账在新版本健康并完成切换后
运行。

### 5.1 更新 Canvas candidate

```bash
cd "$standalone_root"
./deploy/canvas-release.sh \
  --slot candidate \
  --web-image "$canvas_web_image" \
  --api-image "$canvas_api_image" \
  --route-file "$canvas_route_file" \
  --reconcile-bearer-file "$bearer_file"
```

### 5.2 更新 Canvas stable

```bash
cd "$standalone_root"
./deploy/canvas-release.sh \
  --slot stable \
  --web-image "$canvas_web_image" \
  --api-image "$canvas_api_image" \
  --route-file "$canvas_route_file" \
  --reconcile-bearer-file "$bearer_file"
```

### 5.3 更新官方 Sub2API

```bash
cd "$standalone_root"
./deploy/sub2api-release.sh \
  --image "$sub2_image" \
  --route-file "$canvas_route_file" \
  --reconcile-bearer-file "$bearer_file"
```

不指定 `--reconcile-report` 时，报告自动写到：

```text
state/<stable|candidate>/reconcile/reconcile-UTC时间-进程号.json
```

报告目录权限为 `0700`，报告权限为 `0600`。dry-run 只验证对账文件参数，不发起带令牌的
请求，也不生成报告。

## 6. 方案二：更新后单独运行对账

如果更新时没有准备 access token，版本更新仍可先通过原有机器门禁。登录后再运行：

```bash
reconcile_report="$operator_dir/reconcile-$(date -u +%Y%m%dT%H%M%SZ).json"

cd "$standalone_root"
./deploy/post-update-reconcile.sh \
  --slot stable \
  --route-file "$canvas_route_file" \
  --bearer-file "$bearer_file" \
  --report "$reconcile_report"
```

核对 candidate 时只把 `--slot stable` 改为 `--slot candidate`。脚本会依次执行：

1. `canvas-verify.sh`，验证容器、镜像、route、ready 和静态资源；
2. `GET /canvas-api[-next]/v1/session`，确认登录用户；
3. `GET /canvas-api[-next]/v1/credentials/candidates`，读取脱敏 Key 元数据及绑定状态；
4. `GET /canvas-api[-next]/v1/config`，交叉检查可用 Key 和当前策略；
5. 生成 canonical SHA-256 和脱敏报告。

全部是 GET 请求。脚本不会读取 Key 详情端点，不访问数据库，不解密 credential，也不
执行任何 POST、PUT、PATCH 或 DELETE。

## 7. 怎么看报告

```bash
jq '{
  status,
  canvas_release,
  user,
  keys,
  config,
  plaintext_key_accessed,
  mutation_performed
}' "$reconcile_report"
```

重要字段：

| 字段 | 含义 | 处理 |
| --- | --- | --- |
| `status=ok` | 所有 active Key 均已绑定，且无失效绑定 | 更新后 Key 对账通过 |
| `status=attention_required` | 契约正确，但存在需人工处理的绑定 | 按下面三类逐项确认 |
| `active_but_unbound_ids` | Sub2API 中 active，但 Canvas 未绑定 | 用户在创作台手动绑定需要使用的 Key |
| `inactive_but_bound_ids` | Canvas 有绑定，但 Sub2API 已禁用/过期 | 换用 active Key；不要自动重新启用 |
| `missing_from_official` | Canvas 仍有绑定，但 Sub2API 已删除该 ID | 在创作台解除陈旧绑定并换 Key |
| `metadata_sha256` | 本次脱敏元数据的稳定摘要 | 与变更记录一起保存，便于前后比较 |
| `plaintext_key_accessed=false` | 对账未调用完整 Key 端点 | 必须始终为 `false` |
| `mutation_performed=false` | 对账没有修改两边数据 | 必须始终为 `false` |

存在未绑定或失效 Key 时，脚本生成 `attention_required` 报告但返回成功，因为这属于用户
状态，不代表发布损坏。认证失败、响应契约变化、重复 Key ID、Key 集合矛盾、响应出现
secret 字段或报告无法安全写入时，脚本返回非零。

## 8. 对账失败是否自动回滚

不会。执行顺序是：新版本通过健康门禁并完成切换，然后执行用户级对账。若对账失败：

1. 发布命令返回非零并打印失败报告路径；
2. 已通过机器健康检查的新版本保持运行；
3. 不自动回滚数据库或镜像；
4. 操作员先判断是 access token 过期、用户状态问题还是 API 契约变化，再决定是否只回滚
   本次更新的那一边。

这样不会因为某个用户临时登出而把健康服务反复回滚。失败报告只记录受控原因码，不保存
HTTP body、Authorization header 或令牌。

如果最前面的 `canvas-verify.sh` 已经失败，脚本会在发起用户级 HTTP 对账前停止，因此
不会生成用户级失败报告；此时直接保留机器验证输出并按 Canvas 故障处理。

## 9. 完成后清理令牌

报告保留，短期 access token 文件立即删除：

```bash
rm -f -- "$bearer_file"
unset bearer_file reconcile_report
```

不要删除 Canvas PostgreSQL volume、对象目录、credential 主密钥或对账报告。后续每次
维护都用当时的新 access token 重复第 4 至第 9 节。

## 10. 明确禁止的“同步”方式

- 不批量读取或复制完整 Key；
- 不让 Canvas 直连 Sub2API 数据库；
- 不把官方数据库 dump 恢复到 Canvas 数据库；
- 不把 token、Key、ciphertext、nonce 或主密钥写进命令行、日志和报告；
- 不自动启用、替换或删除用户绑定；
- 不把 stable 生产数据库定期克隆到 candidate 当作日常“同步”。

如果后续 Sub2API 改变 profile/key/config HTTP 契约，应先在 candidate 更新适配并通过对账
测试，再更新正式 Sub2API；不能用跨库读取绕过契约变化。
