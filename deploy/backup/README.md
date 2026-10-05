# 每日数据库备份

独立的宿主机维护程序 `cmd/backup`，通过 systemd timer 每天 **东京时间 04:00** 运行。
服务器使用 UTC 时对应 19:00；不依赖服务器的默认时区。停机错过的任务在开机后补跑，
失败后每 15 分钟重试，三小时内最多启动三次。不会停止 API、SSO 或 PostgreSQL。

默认范围只有 **`ourtaiko_fanmade`**，包含所有业务、auth 和 internal schema。
SSO 数据库、数据库角色、环境配置和 S3 歌曲文件不在这个归档里。配置与密钥必须另外保管，
尤其是 `SESSION_ENCRYPTION_KEY`。歌曲替换后会删除旧对象，恢复旧数据库不能找回已删除的歌曲文件；
这不是整个站点的时间点恢复方案。

## 存放与保留

| 项目 | 默认值 |
| --- | --- |
| 本地 | `/var/backups/ourtaiko-fanmade/daily/<UTC时间-随机串>/` |
| S3 | `s3://<S3_BUCKET>/fanmade/backups/postgresql/ourtaiko_fanmade/<UTC时间-随机串>/` |
| 区域 | `ap-northeast-1`，读取现有 `AWS_REGION` |
| 内容 | 压缩的 `database.dump` 和记录 SHA-256、大小、时间、PostgreSQL 镜像的 `manifest.json` |
| 保留时间 | 两处均保留最近 30 天；不是只保留 30 个文件 |
| 状态 | 本地 `daily/status.json`：最近尝试、最近成功、备份 ID、错误 |

使用运行中 PostgreSQL 容器的 `pg_dump` 创建在线一致性快照，读取该容器的精确镜像，
再用同镜像的 `pg_restore --file=/dev/null` 完整读取归档。归档在本地校验通过后才正式命名。
上传 S3 时启用 AES256 服务端加密和 SHA-256 校验，并下载读回比较全部字节的 SHA-256，
最后写入 manifest 作为远端完成标记。S3 对象保持私有，不通过歌曲下载接口暴露。

只有新备份通过校验且 S3 上传读回成功后，才会删除超过保留期限的旧备份。
S3 失败会保留已验证的本地副本，不会清理旧备份。进程锁阻止并发执行。
本地目录权限 0700，归档和配置 0600。历史手动备份目录不受自动清理影响。
清理只识别专用目录/前缀内程序生成的文件，不会操作 `fanmade/production/` 歌曲资源。
如果桶启用了 S3 Versioning，删除当前对象不会删除非当前版本；需另配该备份前缀的生命周期规则。

## 安装或更新

要求 Linux/systemd、Docker（支持 BuildKit 本地输出）、现有 PostgreSQL 容器，
以及 `/etc/ourtaiko-fanmade/docker/storage.env` 中的 S3 配置和凭据。
不需要安装 AWS CLI，也不需要给 API 容器挂载 Docker socket。

```sh
cd /opt/ourtaiko-fanmade/src/backend
sudo bash deploy/backup/install.sh
```

安装器构建独立程序，保留旧程序和 unit 文件，然后立即执行一次真实备份。
首次备份成功后才启用 timer。更新时不会覆盖已有 `/etc/ourtaiko-fanmade/backup.env`。
这一步独立于 API 镜像发布；更新备份程序时重新执行安装器，不必重启 API。
安装失败时检查下方日志；修复后重新安装。保存的旧安装文件位于
`/var/backups/ourtaiko-fanmade/backup-install-*/`，不参加每日保留清理。

配置示例见 [backup.env.example](backup.env.example)。S3 桶与区域、现有 AWS 凭据由
`storage.env` 提供，不要复制到 Git 或命令行。身份需要备份前缀的 `s3:PutObject`、
`s3:GetObject`、`s3:DeleteObject`，以及限定此前缀的 `s3:ListBucket`。
`BACKUP_S3_PREFIX` 只能位于 `fanmade/backups/` 下，数据库名会自动追加。
关闭 S3 可设置 `BACKUP_S3_ENABLED=false`，此时成功状态只代表本地备份。
修改本地目录时应保持在 `/var/backups/ourtaiko-fanmade/` 下，否则需同步修改 service 的 `ReadWritePaths`。

## 查看状态和手动补备份

```sh
sudo systemctl list-timers ourtaiko-fanmade-backup.timer
sudo systemctl status ourtaiko-fanmade-backup.service --no-pager
sudo journalctl -u ourtaiko-fanmade-backup.service -n 30 --no-pager
sudo cat /var/backups/ourtaiko-fanmade/daily/status.json
sudo systemctl start ourtaiko-fanmade-backup.service
```

oneshot 成功后显示 inactive 是正常的，应核对最后结果和 `lastSuccess`。
配置错误或并发锁失败等早期错误由 systemd 记录，可能尚未写入 status.json。
`lastSuccess` 表示备份与上传成功；若随后清理失败，`error` 仍会记录错误。
任务无额外邮件或消息通知，应在日常运维中检查最近成功时间和磁盘空间。
进程崩溃遗留的 `.partial-*` 不会当成成功备份；确认没有任务运行后可手动检查清理。

## 恢复演练

日常完整读取检查能发现损坏，但不能代替数据库恢复演练。应定期将归档恢复到**新建的临时库**，
检查 schema、业务行数与查询结果。不要把生产库作为演练目标。
从 S3 取回备份时先下载同目录的 manifest 和 dump，并核对文件大小和 SHA-256。

以下在服务器 root shell 中执行，把 `backup_dir` 改为已经验证的备份目录。
示例 `createdb` 若发现同名库会失败并停止，不会覆盖已有库。

```sh
set -euo pipefail
backup_dir=/var/backups/ourtaiko-fanmade/daily/REPLACE_WITH_BACKUP_ID
restore_db="fanmade_restore_check_$(date -u +%Y%m%d%H%M%S)"
docker exec ourtaiko-postgres createdb -U postgres -T template0 -O ourtaiko "$restore_db"
docker exec -i ourtaiko-postgres pg_restore -U postgres --exit-on-error --single-transaction \
  --dbname="$restore_db" < "$backup_dir/database.dump"
docker exec ourtaiko-postgres psql -U postgres -d "$restore_db" -v ON_ERROR_STOP=1 \
  -c 'SELECT max(version) FROM internal.schema_migrations;' \
  -c 'SELECT count(*) AS charts FROM public.charts;' \
  -c 'SELECT count(*) AS scores FROM public.scores;'
# 核对通过后，只删除本次新建的临时库。
docker exec ourtaiko-postgres dropdb -U postgres "$restore_db"
```

恢复到新服务器时先部署匹配的 PostgreSQL 大版本、所需数据库角色与扩展。
业务表权限依赖已有角色（例如 `ourtaiko`、`ourtaiko_viewer`），归档不包含角色密码。
生产灾难恢复还需要匹配的应用、配置、S3 文件及 SSO；停写、恢复和切换应另行安排。

## 停用与回退

```sh
sudo systemctl disable --now ourtaiko-fanmade-backup.timer
sudo systemctl stop ourtaiko-fanmade-backup.service
```

以上保留全部备份。若要回退程序，把安装器保留的旧二进制与 unit 文件放回原路径，
执行 `systemctl daemon-reload`，再启动一次手动备份核验后启用 timer。
首次安装没有旧文件可恢复，停用即可；不要删除备份或数据库。
