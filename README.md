# 工作台账系统（Work Ledger · Go 版）

员工每天登录系统填写工作台账；管理员维护人员、职务、多级部门与分类；系统可**一键调用 AI 生成周报、年度总结和任意时段总结**，并提供供外部 AI 拉取数据的接口。支持 TOTP 二次验证、CSV / Excel / JSON 导入导出、整库备份恢复。

本项目由 PHP 版 1:1 迁移而来：**单个可执行文件**，内嵌页面与静态资源，无需 PHP、Nginx 即可运行。默认使用内置 SQLite，无需数据库服务；也可以改用 **MySQL 8.0+ / MariaDB 10.5+ / PostgreSQL 12+**（见 [数据库](#13-数据库sqlite--mysql--mariadb--postgresql)）。密码哈希、TOTP 密钥、备份文件格式与 PHP 版兼容。

- **技术栈**：Go（标准库 `net/http` / `html/template` / `database/sql`）；数据库驱动均为纯 Go、无需 CGO：SQLite（`modernc.org/sqlite`）、MySQL / MariaDB（`go-sql-driver/mysql`）、PostgreSQL（`jackc/pgx`）
- **前端**：服务端渲染 + Tabler 1.7.1 + Tabler Icons 3.49 + ApexCharts / Tom Select / Litepicker，全部本地化，**不依赖任何 CDN**
- **交互**：所有写操作统一为 Ajax `POST + JSON + action`
- **平台**：Linux amd64 / arm64（systemd）、Windows amd64（Windows 服务）

---

## 目录

1. [一键部署](#1-一键部署)
2. [日常管理：wlctl](#2-日常管理wlctl)
3. [配置 AI 与一键生成总结](#3-配置-ai-与一键生成总结)
4. [功能清单](#4-功能清单)
5. [角色与数据权限](#5-角色与数据权限)
6. [AI 数据接口 /api/v1](#6-ai-数据接口-apiv1)
7. [导入导出](#7-导入导出)
8. [备份与恢复](#8-备份与恢复)
9. [HTTPS 与反向代理](#9-https-与反向代理)
10. [从 PHP 版迁移](#10-从-php-版迁移)
11. [开发说明](#11-开发说明)
12. [常见问题](#12-常见问题)
13. [数据库：SQLite / MySQL / MariaDB / PostgreSQL](#13-数据库sqlite--mysql--mariadb--postgresql)

---

## 1. 一键部署

### Linux（Ubuntu / Debian / CentOS / 麒麟等，需 systemd）

```bash
curl -fsSL https://github.com/yooer/work-ledger/raw/main/deploy/install.sh | sudo bash
```

带参数：

```bash
curl -fsSL https://github.com/yooer/work-ledger/raw/main/deploy/install.sh | sudo bash -s -- \
  --listen 127.0.0.1:8080 --admin-password '至少8位的强密码'
```

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--version` | 最新版 | 安装指定版本，如 `v1.0.0` |
| `--listen` | `127.0.0.1:8080` | 监听地址；直接对外可用 `0.0.0.0:8080`，推荐配合反向代理 |
| `--admin-user` | `admin` | 管理员登录名 |
| `--admin-password` | 随机生成并打印 | 管理员初始密码（至少 8 位） |
| `--home` | `/home/work-ledger` | 运行目录 |
| `--user` | `work-ledger` | 运行账号（系统用户，不可登录） |
| `--base-url` | `https://github.com/yooer/work-ledger` | 下载源 |
| `--demo` | 关闭 | 同时创建演示部门、人员和台账 |
| `--db-driver` | `sqlite` | 数据库类型：`sqlite` / `mysql`（MySQL、MariaDB）/ `postgres` |
| `--db-host` `--db-port` `--db-name` `--db-user` `--db-password` | — | 使用 MySQL / PostgreSQL 时的连接信息（库需事先建好，见[第 13 节](#13-数据库sqlite--mysql--mariadb--postgresql)）；密码也可用环境变量 `WL_DB_PASSWORD` 传入 |
| `--db-sslmode` | — | 可选，见第 13 节 |

脚本会：识别 CPU 架构 → 下载程序与 `sha256sums.txt` 并校验 → 创建系统用户和目录 → 生成配置文件（随机生成 AI 接口令牌，权限 600）→ 初始化数据库并创建管理员 → 注册 `work-ledger.service`（开机自启）并启动 → 创建 `/usr/local/bin/wlctl`。

**已安装的机器再次运行脚本会自动转为升级。**

部署后的目录结构：

```text
/home/work-ledger/
├─ bin/
│  ├─ work-ledger          当前程序（wlctl 指向它）
│  └─ work-ledger.prev     升级前的旧版本（用于回滚）
├─ config/.env            配置文件（权限 600）
├─ data/app.sqlite        数据库（及 -wal / -shm）
├─ backups/               本地快照、恢复前快照、升级前数据库备份
├─ logs/app.log           运行日志（同时写入 journald）
└─ tmp/
```

### Windows（Windows Server 2016+ / Windows 10+，64 位）

以**管理员身份**打开 PowerShell：

```powershell
irm https://github.com/yooer/work-ledger/raw/main/deploy/install.ps1 | iex
```

带参数：

```powershell
& ([scriptblock]::Create((irm https://github.com/yooer/work-ledger/raw/main/deploy/install.ps1))) -Listen 127.0.0.1:8080 -AdminPassword '至少8位'
```

安装到 `C:\ProgramData\work-ledger\`（子目录与 Linux 相同），注册为 Windows 服务 `WorkLedger`（自动启动、失败自动重启），并把 `bin` 目录加入系统 PATH，可直接使用 `wlctl`。

### 离线部署

在能访问外网的机器上从 Release 页面下载对应程序（`work-ledger-linux-amd64` 等）和 `sha256sums.txt`，拷贝到服务器后：

```bash
sudo mkdir -p /home/work-ledger/{bin,config,data,backups,logs,tmp}
sudo install -m 755 work-ledger-linux-amd64 /home/work-ledger/bin/work-ledger
sudo cp .env.example /home/work-ledger/config/.env   # 按需修改
sudo useradd --system --home-dir /home/work-ledger --no-create-home --shell /usr/sbin/nologin work-ledger
sudo chown -R work-ledger:work-ledger /home/work-ledger && sudo chmod 600 /home/work-ledger/config/.env
sudo -u work-ledger WL_HOME=/home/work-ledger /home/work-ledger/bin/work-ledger install --admin-password='至少8位'
sudo WL_HOME=/home/work-ledger /home/work-ledger/bin/work-ledger service install --user work-ledger
sudo systemctl start work-ledger
```

离线升级：`sudo wlctl upgrade --file ./work-ledger-linux-amd64 --sha256 <校验值>`。

---

## 2. 日常管理：wlctl

| 命令 | 说明 |
| --- | --- |
| `wlctl status` | 服务状态、版本、健康检查、数据库大小、最近备份、是否可回滚 |
| `sudo wlctl start` / `stop` / `restart` | 启停服务（启动后自动做健康检查） |
| `wlctl logs [-n 200] [-f]` | 查看日志（Linux 读取 journald，Windows 读取 `logs\app.log`） |
| `wlctl version` | 当前版本 |
| `wlctl check` | 检查最新版本（读取 `releases/latest/download/version.json`，不依赖 GitHub API） |
| `sudo wlctl upgrade [vX.Y.Z]` | 升级到最新或指定版本 |
| `sudo wlctl rollback [--with-db]` | 回滚到升级前的程序（`--with-db` 同时恢复升级前数据库） |
| `wlctl backup` | 立即备份数据库：SQLite 为数据库文件 `backups/manual-*.sqlite`，MySQL / PostgreSQL 为 `backups/manual-*.json.gz`（与页面备份同格式） |
| `sudo wlctl migrate-db --driver …` | 把全部数据迁移到 MySQL / MariaDB / PostgreSQL（或迁回 SQLite），见[第 13 节](#13-数据库sqlite--mysql--mariadb--postgresql) |
| `sudo wlctl uninstall [--purge]` | 移除系统服务；默认保留数据，`--purge` 删除整个运行目录 |

**升级流程**（`wlctl upgrade`）：下载新程序 → 校验 sha256 → 试运行新程序 → 备份数据库（SQLite 用 `VACUUM INTO` 生成 `backups/pre-upgrade-*.sqlite`，MySQL / PostgreSQL 生成 `backups/pre-upgrade-*.json.gz`）→ 停止服务 → 旧程序改名为 `work-ledger.prev` → 替换新程序 → 启动 → 30 秒内检查 `/api/v1/health`。**健康检查失败会自动换回旧程序并恢复升级前的数据库**。

升级源由配置项 `UPDATE_BASE_URL` 决定，默认 `https://github.com/yooer/work-ledger`。

---

## 3. 配置 AI 与一键生成总结

编辑 `config/.env`：

```dotenv
AI_API_KEY=你的大模型密钥
AI_BASE_URL=https://api.deepseek.com/v1     # 任何 OpenAI Chat Completions 兼容服务
AI_MODEL=deepseek-chat
AI_TIMEOUT=120
```

保存后执行 `sudo wlctl restart`。

### 手动一键生成（推荐的日常用法）

1. 先把本周期的台账补充、修改完整；
2. 打开 **周报中心**（或年度总结 / 时段总结），确认左侧预览的周期、范围和统计；
3. 选择写作风格（精简 / 标准 / 详细），点击 **一键生成总结**；
4. 生成结果（Markdown）自动保存到历史记录，可复制、打印。台账修改后可以再次生成，每次都会保存一个新版本。

**统计范围**：管理员可选全公司、任意部门、任意人员；部门经理 / 主管领导可选本人、**我的团队（含全部下属）**、分管部门或某个团队成员；普通员工只统计本人。

**导出本周期台账**：周报中心右上角“导出本周期台账”可直接下载当前周期、当前统计范围的台账表格（Excel 或 CSV），列与台账页导出一致。

### 周期设置

管理员在 **系统与 AI → 周报统计周期** 设置“一周”的起止星期，例如 **周五至周四**（上周五到本周四为一周）。周报中心、工作台、API 和自动生成都会按此周期对齐，修改后不影响历史周报。

### 自动生成（默认关闭）

在 **系统与 AI → 自动生成全公司总结** 中可以开启：

- **周报**：上一周期结束后第 N 天（默认次日）的指定时间生成全公司周报；
- **年度总结**：每年 1 月 N 日（默认 2 日）生成上一年度全公司总结。

同一周期已有全公司总结（包括手动生成的）时不会重复生成；服务停机错过时间点的，启动后补生成；失败 30 分钟后重试，最多 5 次，最近一次结果显示在设置页。

---

## 4. 功能清单

**工作台账**：工作分类（一级）与子分类（二级）、工作内容（≤5000 字）、工作日期（精确到分钟）、参与人员（多选）、难易度 1–5、次数 1–100、记录创建者 / 创建时间 / 最后编辑用户 / 最后编辑时间（自动写入）。每条台账保存创建时的部门快照，员工调岗不影响历史统计。

**分类**：工作分类由管理员统一设置，每个一级分类属于一个部门（必选），不同部门可以有同名分类；子分类跟随所属一级分类。员工填台账时可选**本部门及全部下级部门**的分类（上级、平级部门的分类不可选）；升级前已有的分类显示为“未指定部门”，在管理员逐个指定之前对所有部门可用。不同部门有同名分类时，台账列表、导出和报告中显示为“名称（部门）”。任何一级工作分类都可以建立子分类；分类下有启用的子分类时，填台账必须选择子分类。“集客业务”勾选项只作为报告中的集客统计口径。

**数据完整性**：分类、子分类、部门、员工均**不提供删除**，只能停用；台账可由管理员、创建者本人删除（领导删除下属台账需在设置中开启），删除会写入系统日志；子分类创建后不能更换所属分类。管理员可在“数据备份”页**一键清空台账数据**（试运行结束时清掉测试数据）：工作记录和参与人员必清，分类、已生成总结可选；需输入管理员密码和“确认清空”，清空前自动创建 `pre-clear-*` 完整备份；人员、部门、系统设置不受影响。

**人员与部门**：部门不限层级（如 分公司 / 部门 / 班组），各处显示完整路径；**职务**可在“职务管理”中自定义（预置系统管理员、分公司总经理、分公司副总经理、部门经理、部门副经理、主管、员工），每个职务绑定一个权限级别（系统管理员 / 部门管理者 / 普通成员），人员的权限由职务决定；每个人可设置一名**主管领导**（可跨部门，系统会阻止循环汇报），“组织架构”页以树形展示汇报关系；账号可停用、重置密码、重置 2FA；不能停用自己，系统至少保留一名启用管理员。停用或重置密码会立即让该员工已登录的会话失效。

**总结**：周报（可配置周期）、年度（12 个月 + 4 个季度分布）、任意时段（本月 / 上月 / 本季度 / 上季度 / 本年 / 上年快捷范围，自动识别整月 / 整季 / 整年标签）；ApexCharts 图表；AI 一键生成并保存历史。

**安全**：bcrypt 密码、服务端会话（SQLite）、CSRF、登录失败限流、TOTP（±1 步时钟偏差、防重放）、8 个一次性恢复码、`REQUIRE_2FA` 强制绑定、Bearer Token 保护 API。

---

## 5. 角色与数据权限

权限由职务的**权限级别**决定：下表“部门管理者”即原“部门经理”角色，“普通成员”即原“员工”。

| 功能 | 系统管理员 | 部门管理者 | 主管领导（有下属的任何人） | 普通成员 |
| --- | --- | --- | --- | --- |
| 填写台账、我的台账 | ✅ | ✅ | ✅ | ✅ |
| 查看台账（团队台账） | 全部 | 本人 + **本部门及所有下级部门** | 本人 + **直接和间接下属** | 仅本人 |
| 查看参与的台账 | — | ✅ 只读 | ✅ 只读 | ✅ 只读（我的台账中标记“参与”） |
| 编辑台账 | 全部 | 本人创建；开关开启时可改本部门的 | 本人创建；开关开启时可改下属的 | 仅本人创建 |
| 删除台账 | 全部 | 本人创建；开关开启时可删本部门的 | 本人创建；开关开启时可删下属的 | 仅本人创建 |
| 导出台账 | 全部 / 任意筛选 | 本人 + 本部门 | 本人 + 下属 | 仅本人 |
| 生成总结 | 全公司 / 任意部门 / 任意人员 | 本人 / 本部门 / 部门成员 | 本人 / 我的团队 / 下属 | 本人 |
| 导入、备份恢复、人员 / 部门 / 组织架构 / 分类、系统设置 | ✅ | ❌ | ❌ | ❌ |

- 部门经理同时也有下属时，可见范围取两者合并。部门范围按台账**创建时的部门快照**计算；下属范围按记录人计算。
- “我的台账”、个人总结包含本人创建的和本人作为**参与人员**的台账；团队台账、团队总结包含团队成员创建或参与的台账（按记录去重）。参与人员只能查看，不能修改、删除。
- 编辑、删除的两个开关在 **系统与 AI → 领导权限**：“允许修改”默认开启，“允许删除”默认关闭。下属只是参与人员、由团队外的人创建的记录，领导只能查看。修改时记录人不变，“最后编辑用户”记为修改人；删除不可撤销，系统日志会记录删除人与内容摘要。
- 主管领导关系由管理员在 **人员管理** 中设置（每人一名主管领导），在 **组织架构** 页查看。

> 与 PHP 版的差异：PHP 版中部门经理的权限与普通员工相同；Go 版按原始需求开放了“查看 / 导出本部门台账、生成本部门总结”，并新增主管领导（汇报关系）。

---

## 6. AI 数据接口 /api/v1

协议与 PHP 版完全一致，所有接口需要请求头 `Authorization: Bearer <AI_API_TOKEN>`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/health` | 健康检查（无需令牌） |
| GET | `/api/v1/weekly` | `week_start`（自动对齐周期）、`week_end`、`department_id`、`employee_id`、`include_sub_departments` |
| POST | `/api/v1/weekly-summary` | 同上 + `style`（concise/normal/detailed）、`dry_run`、`save` |
| GET | `/api/v1/yearly` | `year`（默认当年）、`department_id`、`employee_id` |
| POST | `/api/v1/yearly-summary` | 同上 + `style`、`dry_run`、`save` |
| GET | `/api/v1/period` | `start_date`、`end_date`（默认本月）、`department_id`、`employee_id` |
| POST | `/api/v1/period-summary` | 同上 + `style`、`dry_run`、`save` |

指定 `employee_id` 时，统计该员工创建的和作为参与人员的台账（与页面上的个人总结口径一致）。

```bash
curl -s -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:8080/api/v1/weekly?week_start=2026-03-06"

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"week_start":"2026-03-06","style":"normal","save":true}' \
  http://127.0.0.1:8080/api/v1/weekly-summary
```

`dry_run=true` 只返回统计数据与提示词，不调用大模型，便于调试。错误响应格式：`{"ok":false,"error":{"code":"...","message":"..."}}`。

---

## 7. 导入导出

**导出**：在全部台账 / 我的台账 / 团队台账页按当前筛选条件（日期、分类、子分类、人员、部门、关键词）导出 CSV（UTF-8 BOM，Excel 直接打开）、Excel（.xlsx）或 JSON，单次最多 `EXPORT_MAX_ROWS`（默认 20000）条。导出文件可再次导入。

**导入**（管理员，`/records/import`）：

- 支持 CSV、XLSX（第一个工作表）、JSON（本系统导出或含 `records` 数组）；
- 表头：`工作分类,工作内容,日期,参与人员,难易度,次数,子分类`，兼容老系统的 `集客分类` / `集客项目` 列名，表头顺序不限，支持常用别名；
- 可选审计列：记录创建者（`Creator` / `记录人`）、记录创建时间（`创建于` / `记录创建时间`）、最后修改者（`Last editor` / `最后编辑用户`）、最后修改时间（`Last edited at` / `最后编辑时间`），有则按文件写入，没有则分别用记录人、工作日期补齐；
- 自动识别 UTF-8 / GBK / GB18030 / UTF-16、BOM、逗号 / 分号 / 制表符分隔；
- 日期支持 `YYYY-MM-DD HH:MM[:SS]`、`YYYY/MM/DD`、`YYYY-MM-DD` 与 Excel 日期序列；
- 分类按记录人部门匹配（本部门 + 下级部门 + 未指定部门的分类）；匹配到多个同名分类时优先记录人本部门的，仍无法确定则该行报错，可写成 `名称（部门名）` 指定（导出文件中的同名分类就是这种写法，可直接再导入）；
- 可自动创建分类（建在记录人所在部门下）、子分类和历史人员（停用状态）；分类下有子分类但该行未填写时归入“未分类（历史导入）”；
- 逐行校验，失败行不写库并显示行号与原因；按记录指纹去重，**指纹算法与 PHP 版一致**，两个系统之间重复导入也会跳过。

---

## 8. 备份与恢复

管理员在 **数据备份** 页面：

- 下载完整备份（`.json.gz` 或 `.json`），包含全部 11 张业务表（含职务、密码哈希与 TOTP 密钥，请按敏感文件保管）；不包含 `.env`、AI Key、FTP 密码、日志；
- 创建本地快照（`backups/`）；
- **每日自动备份与 FTP 上传**（默认关闭）：每天在设定时间（默认 02:00）生成一份压缩备份 `backups/auto-YYYYMMDD-HHMMSS.json.gz`（与“下载压缩备份”同格式，可直接用于恢复）；填写 FTP 服务器后自动上传，可选显式 TLS（FTPS），远程目录不存在时自动创建。本地、FTP 分别设置保留份数（默认 7 / 30，填 0 不删除），超出时只删除最旧的 `auto-*` 文件。提供“测试 FTP 连接”和“立即备份并上传”按钮，页面显示最近一次执行结果；服务停机错过时间点的，当天启动后会补做。FTP 密码保存在系统设置中，不会写进备份文件，恢复备份时保留当前密码；
- 上传备份**覆盖恢复**：校验格式 / 版本 / 字段 / 行数 / 至少一名启用管理员 → 自动创建 `pre-restore-*` 快照 → 事务内恢复（失败回滚）→ 所有会话失效，用备份中的管理员账号重新登录。

**PHP 版导出的备份可以直接在 Go 版恢复**：恢复时按原角色自动对应职务（系统管理员 / 部门经理 / 员工），主管领导等 Go 版新增信息为“未设置”。Go 版备份完整保留职务、多级部门、主管领导等信息，**不再保证能恢复到 PHP 版**（恢复到 PHP 版会丢失职务，第三级及以下部门可能显示不正确）。

命令行也可以随时 `wlctl backup` 生成数据库备份（SQLite 为数据库文件副本，MySQL / PostgreSQL 为 `.json.gz`）。建议定期把 `backups/` 转存到离线位置。

---

## 9. HTTPS 与反向代理

默认只监听 `127.0.0.1:8080`，建议用 Nginx 或 Caddy 提供 HTTPS。

**Nginx**：

```nginx
server {
    listen 443 ssl http2;
    server_name ledger.example.com;
    ssl_certificate     /etc/ssl/ledger.pem;
    ssl_certificate_key /etc/ssl/ledger.key;
    client_max_body_size 60m;              # 导入 / 恢复上传

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 300s;           # AI 生成可能较慢
    }
}
```

**Caddy**：

```caddyfile
ledger.example.com {
    reverse_proxy 127.0.0.1:8080 {
        transport http {
            read_timeout 300s
        }
    }
}
```

使用反向代理后，把 `config/.env` 中的 `APP_URL` 改为 `https://ledger.example.com`。通过 HTTPS 访问时（`X-Forwarded-Proto: https`），会话 Cookie 会自动加上 Secure 属性。

---

## 10. 从 PHP 版迁移

两种方式任选：

1. **直接使用 PHP 版数据库**：停止 PHP 版，把 `database/app.sqlite`（如存在 `app.sqlite-wal`、`app.sqlite-shm` 也一并复制）复制为 `/home/work-ledger/data/app.sqlite`（属主改为 `work-ledger`），执行 `sudo wlctl restart`。启动时会自动补齐 Go 版需要的会话表，原有账号密码、2FA、台账、报告全部可用。
2. **通过备份文件**：在 PHP 版 `/backup` 下载 `.json.gz`，在 Go 版“数据备份”页上传恢复。

---

## 11. 开发说明

```text
cmd/work-ledger/      程序入口：serve / install / ctl / service
internal/
  config/             读取 .env
  db/                 数据库连接与方言转换（SQLite / MySQL / PostgreSQL）、建表（schema*.sql）、升级
  dbtest/             测试用数据库（默认 SQLite；WL_TEST_DRIVER 指定 MySQL / PostgreSQL）
  session/            服务端会话（sessions 表）
  web/                请求上下文、模板渲染、Ajax 分发（POST + JSON + action）、守卫、Markdown、分页
  validator/          每个 action 的入参验证器（格式 / 范围 / 必填）
  controller/         只做：解析 → 验证 → 调 service → 返回；禁止访问数据库
  service/            全部业务规则与 SQL
  ctl/                wlctl 与 systemd / Windows 服务
  totp/ password/ xlsx/ timeutil/ apperr/ model/
web/
  templates/          layouts / partials / pages（无 <style>、无 style=""）
  assets/js css image libs/   业务脚本、页面样式、图片、第三方库（Tabler 等）
  static/img/<模块>/   模块专属图片
deploy/               install.sh / install.ps1
scripts/check-rules.sh 开发约束自检
```

开发运行：

```bash
mkdir -p dev/config && cp .env.example dev/config/.env   # 设置 APP_ENV=local、APP_DEBUG=true
export WL_HOME=$PWD/dev
go run ./cmd/work-ledger install --admin-password=Admin12345 --demo
go run ./cmd/work-ledger serve
```

检查与测试：

```bash
bash scripts/check-rules.sh   # Controller 不碰数据库、无内联样式、无外部 CDN、Ajax 统一入口
go vet ./... && go test ./...

# 在 MySQL / PostgreSQL 上跑同一套测试（每个测试自动建立并删除临时库，需要建库权限）
WL_TEST_DRIVER=postgres WL_TEST_HOST=127.0.0.1 WL_TEST_USER=postgres WL_TEST_PASSWORD=... go test -p 1 ./internal/...
```

CI 会在 SQLite、MySQL 8.0、MariaDB 10.11、PostgreSQL 12 / 16 上各跑一遍全部测试。

**Ajax 约定**：每个模块一个入口（如 `POST /records/ajax`），请求体为 JSON，必须带 `action`；CSRF 令牌放在请求头 `X-CSRF-Token`。文件上传（导入、恢复）使用 `multipart/form-data` 并同样带 `action`；文件下载使用 GET；`/api/v1/*` 保持原协议。

**发布**：推送 `v*` 标签后，GitHub Actions 自动测试、编译 linux-amd64 / linux-arm64 / windows-amd64，生成 `sha256sums.txt` 与 `version.json` 并创建 Release。

---

## 12. 常见问题

**启动提示“系统尚未初始化”**：执行 `work-ledger install --admin-password=...`（一键脚本会自动执行）。

**AI 生成失败**：`wlctl logs` 查看原因。常见为服务器无法访问大模型服务、`AI_BASE_URL` / `AI_MODEL` 不正确、Key 无权限或超时（调大 `AI_TIMEOUT`，并同步调大反向代理超时）。

**验证器丢失**：使用恢复码登录；恢复码也用完时，由管理员在“人员管理 → 编辑 → 重置二次验证”。

**升级失败怎么办**：`wlctl upgrade` 会自动回滚程序和数据库；也可以手动 `sudo wlctl rollback --with-db`。

**`wlctl check` 失败**：确认服务器能访问 GitHub，或修改 `UPDATE_BASE_URL`；无法联网时可以使用离线升级。

**普通员工看不到同事的台账**：这是权限设计。部门经理可以看本部门，主管领导可以看自己的下属，管理员可以看全部。

---

AI 服务生成的内容会把台账数据发送到所配置的大模型服务商，请遵守本单位的数据安全规定；涉及敏感客户信息时建议使用私有化模型。

---

## 13. 数据库：SQLite / MySQL / MariaDB / PostgreSQL

默认使用内置 SQLite（`data/app.sqlite`），单机几百人规模完全够用，无需任何数据库服务。需要集中管理数据库时，可以改用：

| 数据库 | 最低版本 | `DB_DRIVER` | 默认端口 |
| --- | --- | --- | --- |
| MySQL | 8.0 | `mysql` | 3306 |
| MariaDB | 10.5 | `mysql` | 3306 |
| PostgreSQL | 12 | `postgres` | 5432 |

启动时会检查数据库版本，低于最低版本直接报错退出（部门层级、主管领导依赖递归查询，MySQL 5.7 不支持）。

**1. 事先建好空库和账号**（程序启动时自动建表）：

```sql
-- MySQL / MariaDB
CREATE DATABASE work_ledger CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'wl'@'%' IDENTIFIED BY '强密码';
GRANT ALL PRIVILEGES ON work_ledger.* TO 'wl'@'%';

-- PostgreSQL
CREATE USER wl WITH PASSWORD '强密码';
CREATE DATABASE work_ledger OWNER wl;
```

**2. 配置**（`config/.env`，或一键安装时用 `--db-*` 参数）：

```ini
DB_DRIVER=mysql          # 或 postgres
DB_HOST=192.168.1.10
DB_PORT=3306             # 可省略，默认 3306 / 5432
DB_NAME=work_ledger
DB_USER=wl
DB_PASSWORD="强密码"
DB_SSLMODE=              # 可选：PostgreSQL 为 disable/prefer/require/verify-full（默认 prefer）；MySQL 为 true/false/skip-verify（默认不加密）
```

**3. 把现有 SQLite 数据迁过去**，两种方式任选：

- **迁移命令（推荐）**：

  ```bash
  sudo wlctl migrate-db --driver mysql --host 192.168.1.10 --name work_ledger --user wl
  ```

  密码会提示输入（不回显），也可 `echo '密码' | sudo wlctl migrate-db …`。命令依次：检查目标库可连接、版本满足且为空库（已有数据则拒绝，不会覆盖）→ 暂停服务 → 在 `backups/` 留一份 `pre-migrate-*.json.gz` → 在目标库建表并在一个事务中写入全部数据 → 逐表核对行数 → 改写 `config/.env`（原文件备份为 `.env.bak-时间`）→ 启动服务并做健康检查。任何一步失败都不会修改配置，服务继续使用原数据库。**原 SQLite 文件保持不动**；要改回，用 `sudo wlctl migrate-db --driver sqlite --path 新文件` 迁回，或把 `.env.bak-*` 复制回去并重启（迁移后新产生的数据不会带回）。命令支持任意两种数据库之间互迁。

- **手动（备份 + 恢复）**：
  1. 在“数据备份”页下载压缩备份；
  2. 按第 2 步修改 `config/.env`；
  3. 初始化新库并创建临时管理员：`sudo -u work-ledger env WL_HOME=/home/work-ledger /home/work-ledger/bin/work-ledger install --admin-password=临时密码`；
  4. `sudo wlctl restart`，用 `admin` / 临时密码登录，在“数据备份”页上传第 1 步的备份恢复（恢复后用原来的账号密码登录）。

  注意：FTP 密码不在备份文件中，恢复后需要在数据备份页重新填写。

**使用 MySQL / PostgreSQL 时**：页面备份恢复、每日自动备份与 FTP 上传照常可用；`wlctl backup` 和升级前备份改为生成 `.json.gz`，升级失败时自动换回旧程序并用它恢复数据；“系统与 AI”页显示当前数据库类型和版本。数据库本身的备份、主从、权限等仍建议由 DBA 按常规方式管理。`wlctl uninstall --purge` 只删除运行目录，不会删除 MySQL / PostgreSQL 中的数据库。

