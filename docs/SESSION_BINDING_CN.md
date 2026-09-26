# 独立创作台的登录指纹与更新对账

Sub2API 启用 IP / User-Agent 会话绑定时，独立创作台必须沿用用户登录时的请求指纹。
只转发 Bearer 会导致创作台或更新管理返回 401；经公网 CDN 回调还会把用户 IP 换成服务器 IP。
不要关闭会话绑定来绕过问题。已发生指纹不匹配的会话可能已撤销刷新令牌，需要重新登录。

## 部署前设置（本机 Docker 拓扑）

1. 保持 Canvas API 端口只发布到 `127.0.0.1`，不能直接开放给公网。
2. 路径代理监听本机 `8080`，Sub2API 蓝绿代理保持 `127.0.0.1:18080`。
3. 使用 `docker network inspect canvas-stable_default canvas-candidate_default` 核实网段和桥名。
   仅允许这两个 Docker 网桥访问宿主机内部代理 `172.17.0.1:8080`，不对公网开放端口。
4. 将 `deploy/environments/stable.env`、`candidate.env` 的
   `CANVAS_OFFICIAL_BASE_URL` 改为 `http://host.docker.internal:8080`。
   此地址必须绕过 Cloudflare，不能使用公共站点 URL。
5. 用正常 `canvas-release.sh` 流程重建服务，先 candidate 验证后再 stable。
6. 用同一登录会话验证 `/api/v1/admin/deployments`、`/canvas-api/v1/session` 和
   `/canvas-api/v1/credentials/candidates`，然后运行更新后只读对账。

本机 2026-09-26 已验证的规则如下（换服务器时先核对桥名、网段和 host-gateway）：

```bash
sudo ufw allow in on br-94987ef87b8e from 172.20.0.0/16 to 172.17.0.1 port 8080 proto tcp comment 'Canvas stable internal official API'
sudo ufw allow in on br-c99740d13cc2 from 172.19.0.0/16 to 172.17.0.1 port 8080 proto tcp comment 'Canvas candidate internal official API'
docker exec canvas_candidate_api /usr/local/bin/canvas-healthcheck http://host.docker.internal:8080/health
docker exec canvas_stable_api /usr/local/bin/canvas-healthcheck http://host.docker.internal:8080/health
```

此处不开放 `8080` 的公网访问，不开放 `18080`，也不关闭 UFW。
不要 `docker compose down` 删除网络后仍沿用旧桥名规则；普通 release 不会删除网络。
若必须重建网络，重新读取 network ID（桥名为 `br-` 加 ID 前 12 位），更新上述规则。
移除旧规则使用 `sudo ufw status numbered` 核对后执行 `sudo ufw delete <规则编号>`。

代理旧文件和环境配置备份位于：
`state/operator/proxy-backup-20260926.0SRF4c/`（目录 0700）。
回滚代码应连同网络和环境配置一起评估；旧 Canvas 不支持当前绑定会话，
仅回滚镜像会恢复旧登录故障。无需恢复或覆盖数据库。

## 安全边界

路径代理与 Canvas Gateway 仅传递 User-Agent、CF-Connecting-IP、X-Real-IP、X-Forwarded-For。
Cookie 和无关认证头不复制；Sub2API 仍是最终的身份与权限校验方。
Canvas 认证缓存包含请求指纹，同一个 token 换 IP 或浏览器不能复用已验证缓存。
Canvas 内网和代理属于可信链，新增代理或改变 Sub2API 的可信 IP 解析规则时必须重新验证。

网页更新管理自动创建 0600 的 token 文件及同名 `.headers.json` 会话上下文文件。
发布后的只读对账使用这两者，成功或失败后都删除；不把上下文放到命令行或操作状态接口。
进程被强制终止时应检查 `state/operator/tokens` 的遗留文件并删除已结束操作对应的凭据。

命令行使用 `--reconcile-bearer-file` 时，推荐在本机以同一 User-Agent 登录取得新 token。
如 token 来自浏览器，需在 token 文件旁提供同名 `.headers.json`（0600、当前用户所有，
仅含上述四个字段），填写登录请求的真实上下文。不能通过伪造指纹或无绑定 token 绕过校验。

对账仅核对当前登录用户的 Key 元数据与绑定状态，不复制 Key 明文、不自动绑定，
不进行全站用户数据迁移。首次拆分已完成，无需再次迁移。
`attention_required` 表示核对完成但有未绑定等差异；`failed` 才是对账失败，需查看报告。
