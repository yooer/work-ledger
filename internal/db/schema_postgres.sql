-- 工作台账 PostgreSQL 12+ 数据库结构（由 schema.sql 的 SQLite 结构对应改写）
-- 名称类字段不区分大小写：唯一索引建在 LOWER(...) 上，查询统一写 LOWER(col) = LOWER(?)。
-- 设计原则：人员、部门、工作分类、集客项目分类均不提供物理删除，
-- 历史台账通过外键 RESTRICT 保持完整。


CREATE TABLE IF NOT EXISTS departments (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    parent_id BIGINT NULL,
    is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (parent_id) REFERENCES departments(id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_departments_name ON departments(LOWER(name));
CREATE INDEX IF NOT EXISTS ix_departments_parent ON departments(parent_id);

-- Go 版新增：部门完整路径（不限层级），如“华东分公司 / 集客部 / 一组”。
CREATE OR REPLACE VIEW department_paths AS
WITH RECURSIVE t(id, path, depth) AS (
    SELECT id, CAST(name AS TEXT), 0 FROM departments WHERE parent_id IS NULL
    UNION ALL
    SELECT d.id, CAST(t.path || ' / ' || d.name AS TEXT), t.depth + 1 FROM departments d JOIN t ON d.parent_id = t.id WHERE t.depth < 50
)
SELECT id, path, depth FROM t;

-- Go 版新增：职务（名称可自定义，每个职务绑定一个权限级别 role）。
CREATE TABLE IF NOT EXISTS positions (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'employee' CHECK (role IN ('admin', 'manager', 'employee')),
    is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_positions_name ON positions(LOWER(name));

CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    name TEXT NOT NULL,
    employee_no TEXT NULL,
    role TEXT NOT NULL DEFAULT 'employee' CHECK (role IN ('admin', 'manager', 'employee')),
    department_id BIGINT NULL,
    phone TEXT NULL,
    email TEXT NULL,
    is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    totp_secret TEXT NULL,
    totp_enabled INTEGER NOT NULL DEFAULT 0 CHECK (totp_enabled IN (0, 1)),
    totp_recovery_codes TEXT NULL,
    totp_last_used_step BIGINT NULL,
    last_login_at TEXT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    supervisor_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    position_id BIGINT NULL REFERENCES positions(id) ON DELETE SET NULL,
    FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_users_username ON users(LOWER(username));
CREATE UNIQUE INDEX IF NOT EXISTS ux_users_employee_no ON users(LOWER(employee_no));
CREATE INDEX IF NOT EXISTS ix_users_department ON users(department_id);
CREATE INDEX IF NOT EXISTS ix_users_active ON users(is_active);
CREATE INDEX IF NOT EXISTS ix_users_supervisor ON users(supervisor_id);
CREATE INDEX IF NOT EXISTS ix_users_position ON users(position_id);

CREATE TABLE IF NOT EXISTS work_categories (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    is_customer_project INTEGER NOT NULL DEFAULT 0 CHECK (is_customer_project IN (0, 1)),
    is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    department_id BIGINT NULL REFERENCES departments(id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_work_categories_dept_name ON work_categories(COALESCE(department_id, 0), LOWER(name));
CREATE INDEX IF NOT EXISTS ix_work_categories_department ON work_categories(department_id);

CREATE TABLE IF NOT EXISTS project_categories (
    id BIGSERIAL PRIMARY KEY,
    work_category_id BIGINT NOT NULL,
    name TEXT NOT NULL,
    is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (work_category_id) REFERENCES work_categories(id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_project_categories_name ON project_categories(work_category_id, LOWER(name));
CREATE INDEX IF NOT EXISTS ix_project_categories_work ON project_categories(work_category_id);

CREATE TABLE IF NOT EXISTS work_records (
    id BIGSERIAL PRIMARY KEY,
    work_category_id BIGINT NOT NULL,
    project_category_id BIGINT NULL,
    content TEXT NOT NULL,
    work_at TEXT NOT NULL,
    difficulty INTEGER NOT NULL CHECK (difficulty BETWEEN 1 AND 5),
    work_count INTEGER NOT NULL DEFAULT 1 CHECK (work_count BETWEEN 1 AND 100),
    department_id BIGINT NULL,
    created_by BIGINT NOT NULL,
    created_at TEXT NOT NULL,
    updated_by BIGINT NOT NULL,
    updated_at TEXT NOT NULL,
    import_hash TEXT NULL,
    FOREIGN KEY (work_category_id) REFERENCES work_categories(id) ON DELETE RESTRICT,
    FOREIGN KEY (project_category_id) REFERENCES project_categories(id) ON DELETE RESTRICT,
    FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT,
    FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS ix_work_records_work_at ON work_records(work_at);
CREATE INDEX IF NOT EXISTS ix_work_records_category ON work_records(work_category_id);
CREATE INDEX IF NOT EXISTS ix_work_records_project ON work_records(project_category_id);
CREATE INDEX IF NOT EXISTS ix_work_records_department ON work_records(department_id);
CREATE INDEX IF NOT EXISTS ix_work_records_creator ON work_records(created_by);
CREATE UNIQUE INDEX IF NOT EXISTS ux_work_records_import_hash ON work_records(import_hash);

CREATE TABLE IF NOT EXISTS work_record_participants (
    work_record_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (work_record_id, user_id),
    FOREIGN KEY (work_record_id) REFERENCES work_records(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS ix_participants_user ON work_record_participants(user_id);

CREATE TABLE IF NOT EXISTS weekly_reports (
    id BIGSERIAL PRIMARY KEY,
    week_start TEXT NOT NULL,
    week_end TEXT NOT NULL,
    department_id BIGINT NULL,
    employee_id BIGINT NULL,
    scope_label TEXT NOT NULL,
    content TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'ai' CHECK (source IN ('ai', 'api')),
    model TEXT NULL,
    created_by BIGINT NULL,
    created_at TEXT NOT NULL,
    team_leader_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    FOREIGN KEY (employee_id) REFERENCES users(id) ON DELETE RESTRICT,
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS ix_weekly_reports_week ON weekly_reports(week_start, week_end);
CREATE INDEX IF NOT EXISTS ix_weekly_reports_department ON weekly_reports(department_id);
CREATE INDEX IF NOT EXISTS ix_weekly_reports_employee ON weekly_reports(employee_id);
CREATE INDEX IF NOT EXISTS ix_weekly_reports_team ON weekly_reports(team_leader_id);

CREATE TABLE IF NOT EXISTS annual_reports (
    id BIGSERIAL PRIMARY KEY,
    report_year INTEGER NOT NULL CHECK (report_year BETWEEN 2000 AND 2100),
    department_id BIGINT NULL,
    employee_id BIGINT NULL,
    scope_label TEXT NOT NULL,
    content TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'ai' CHECK (source IN ('ai', 'api')),
    model TEXT NULL,
    created_by BIGINT NULL,
    created_at TEXT NOT NULL,
    team_leader_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    FOREIGN KEY (employee_id) REFERENCES users(id) ON DELETE RESTRICT,
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS ix_annual_reports_year ON annual_reports(report_year);
CREATE INDEX IF NOT EXISTS ix_annual_reports_department ON annual_reports(department_id);
CREATE INDEX IF NOT EXISTS ix_annual_reports_employee ON annual_reports(employee_id);
CREATE INDEX IF NOT EXISTS ix_annual_reports_team ON annual_reports(team_leader_id);

CREATE TABLE IF NOT EXISTS app_settings (
    name TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS period_reports (
    id BIGSERIAL PRIMARY KEY,
    start_date TEXT NOT NULL,
    end_date TEXT NOT NULL,
    period_label TEXT NOT NULL,
    department_id BIGINT NULL,
    employee_id BIGINT NULL,
    scope_label TEXT NOT NULL,
    content TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'ai' CHECK (source IN ('ai', 'api')),
    model TEXT NULL,
    created_by BIGINT NULL,
    created_at TEXT NOT NULL,
    team_leader_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    FOREIGN KEY (employee_id) REFERENCES users(id) ON DELETE RESTRICT,
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS ix_period_reports_range ON period_reports(start_date, end_date);
CREATE INDEX IF NOT EXISTS ix_period_reports_department ON period_reports(department_id);
CREATE INDEX IF NOT EXISTS ix_period_reports_employee ON period_reports(employee_id);
CREATE INDEX IF NOT EXISTS ix_period_reports_team ON period_reports(team_leader_id);

-- 服务端会话表（不参与备份/恢复）
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    user_id BIGINT NULL,
    data TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS ix_sessions_expires ON sessions(expires_at);
