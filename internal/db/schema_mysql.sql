-- 工作台账 MySQL 8.0+ / MariaDB 10.5+ 数据库结构（与 schema.sql 的 SQLite 结构一一对应）
-- 字符集 utf8mb4 + utf8mb4_unicode_ci（不区分大小写，对应 SQLite 的 COLLATE NOCASE）。
-- 日期时间统一以 “YYYY-MM-DD HH:MM:SS” 文本保存，与 SQLite 版一致。

CREATE TABLE IF NOT EXISTS departments (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(191) NOT NULL,
    parent_id BIGINT NULL,
    is_active INT NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    sort_order INT NOT NULL DEFAULT 0,
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE KEY ux_departments_name (name),
    KEY ix_departments_parent (parent_id),
    CONSTRAINT fk_departments_parent FOREIGN KEY (parent_id) REFERENCES departments(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE OR REPLACE VIEW department_paths AS
WITH RECURSIVE t(id, path, depth) AS (
    SELECT id, CAST(name AS CHAR(4000)), 0 FROM departments WHERE parent_id IS NULL
    UNION ALL
    SELECT d.id, CONCAT(t.path, ' / ', d.name), t.depth + 1 FROM departments d JOIN t ON d.parent_id = t.id WHERE t.depth < 50
)
SELECT id, path, depth FROM t;

CREATE TABLE IF NOT EXISTS positions (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(191) NOT NULL,
    role VARCHAR(16) NOT NULL DEFAULT 'employee' CHECK (role IN ('admin', 'manager', 'employee')),
    is_active INT NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    sort_order INT NOT NULL DEFAULT 0,
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE KEY ux_positions_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS users (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(191) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    name VARCHAR(191) NOT NULL,
    employee_no VARCHAR(191) NULL,
    role VARCHAR(16) NOT NULL DEFAULT 'employee' CHECK (role IN ('admin', 'manager', 'employee')),
    department_id BIGINT NULL,
    phone VARCHAR(191) NULL,
    email VARCHAR(191) NULL,
    is_active INT NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    totp_secret VARCHAR(255) NULL,
    totp_enabled INT NOT NULL DEFAULT 0 CHECK (totp_enabled IN (0, 1)),
    totp_recovery_codes TEXT NULL,
    totp_last_used_step BIGINT NULL,
    last_login_at VARCHAR(32) NULL,
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    supervisor_id BIGINT NULL,
    position_id BIGINT NULL,
    UNIQUE KEY ux_users_username (username),
    UNIQUE KEY ux_users_employee_no (employee_no),
    KEY ix_users_department (department_id),
    KEY ix_users_active (is_active),
    KEY ix_users_supervisor (supervisor_id),
    KEY ix_users_position (position_id),
    CONSTRAINT fk_users_department FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    CONSTRAINT fk_users_supervisor FOREIGN KEY (supervisor_id) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT fk_users_position FOREIGN KEY (position_id) REFERENCES positions(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS work_categories (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(191) NOT NULL,
    is_customer_project INT NOT NULL DEFAULT 0 CHECK (is_customer_project IN (0, 1)),
    is_active INT NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    sort_order INT NOT NULL DEFAULT 0,
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    department_id BIGINT NULL,
    dept_key BIGINT AS (COALESCE(department_id, 0)) STORED,
    UNIQUE KEY ux_work_categories_dept_name (dept_key, name),
    KEY ix_work_categories_department (department_id),
    CONSTRAINT fk_work_categories_department FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS project_categories (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    work_category_id BIGINT NOT NULL,
    name VARCHAR(191) NOT NULL,
    is_active INT NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    sort_order INT NOT NULL DEFAULT 0,
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE KEY ux_project_categories_name (work_category_id, name),
    KEY ix_project_categories_work (work_category_id),
    CONSTRAINT fk_project_categories_work FOREIGN KEY (work_category_id) REFERENCES work_categories(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS work_records (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    work_category_id BIGINT NOT NULL,
    project_category_id BIGINT NULL,
    content TEXT NOT NULL,
    work_at VARCHAR(32) NOT NULL,
    difficulty INT NOT NULL CHECK (difficulty BETWEEN 1 AND 5),
    work_count INT NOT NULL DEFAULT 1 CHECK (work_count BETWEEN 1 AND 100),
    department_id BIGINT NULL,
    created_by BIGINT NOT NULL,
    created_at VARCHAR(32) NOT NULL,
    updated_by BIGINT NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    import_hash VARCHAR(64) NULL,
    KEY ix_work_records_work_at (work_at),
    KEY ix_work_records_category (work_category_id),
    KEY ix_work_records_project (project_category_id),
    KEY ix_work_records_department (department_id),
    KEY ix_work_records_creator (created_by),
    UNIQUE KEY ux_work_records_import_hash (import_hash),
    CONSTRAINT fk_work_records_category FOREIGN KEY (work_category_id) REFERENCES work_categories(id) ON DELETE RESTRICT,
    CONSTRAINT fk_work_records_project FOREIGN KEY (project_category_id) REFERENCES project_categories(id) ON DELETE RESTRICT,
    CONSTRAINT fk_work_records_department FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    CONSTRAINT fk_work_records_creator FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_work_records_updater FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS work_record_participants (
    work_record_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (work_record_id, user_id),
    KEY ix_participants_user (user_id),
    CONSTRAINT fk_participants_record FOREIGN KEY (work_record_id) REFERENCES work_records(id) ON DELETE CASCADE,
    CONSTRAINT fk_participants_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS weekly_reports (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    week_start VARCHAR(32) NOT NULL,
    week_end VARCHAR(32) NOT NULL,
    department_id BIGINT NULL,
    employee_id BIGINT NULL,
    scope_label VARCHAR(1000) NOT NULL,
    content MEDIUMTEXT NOT NULL,
    source VARCHAR(16) NOT NULL DEFAULT 'ai' CHECK (source IN ('ai', 'api')),
    model VARCHAR(191) NULL,
    created_by BIGINT NULL,
    created_at VARCHAR(32) NOT NULL,
    team_leader_id BIGINT NULL,
    KEY ix_weekly_reports_week (week_start, week_end),
    KEY ix_weekly_reports_department (department_id),
    KEY ix_weekly_reports_employee (employee_id),
    KEY ix_weekly_reports_team (team_leader_id),
    CONSTRAINT fk_weekly_department FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    CONSTRAINT fk_weekly_employee FOREIGN KEY (employee_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_weekly_creator FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT fk_weekly_team FOREIGN KEY (team_leader_id) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS annual_reports (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    report_year INT NOT NULL CHECK (report_year BETWEEN 2000 AND 2100),
    department_id BIGINT NULL,
    employee_id BIGINT NULL,
    scope_label VARCHAR(1000) NOT NULL,
    content MEDIUMTEXT NOT NULL,
    source VARCHAR(16) NOT NULL DEFAULT 'ai' CHECK (source IN ('ai', 'api')),
    model VARCHAR(191) NULL,
    created_by BIGINT NULL,
    created_at VARCHAR(32) NOT NULL,
    team_leader_id BIGINT NULL,
    KEY ix_annual_reports_year (report_year),
    KEY ix_annual_reports_department (department_id),
    KEY ix_annual_reports_employee (employee_id),
    KEY ix_annual_reports_team (team_leader_id),
    CONSTRAINT fk_annual_department FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    CONSTRAINT fk_annual_employee FOREIGN KEY (employee_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_annual_creator FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT fk_annual_team FOREIGN KEY (team_leader_id) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS app_settings (
    name VARCHAR(191) NOT NULL PRIMARY KEY,
    value MEDIUMTEXT NOT NULL,
    updated_at VARCHAR(32) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS period_reports (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    start_date VARCHAR(32) NOT NULL,
    end_date VARCHAR(32) NOT NULL,
    period_label VARCHAR(255) NOT NULL,
    department_id BIGINT NULL,
    employee_id BIGINT NULL,
    scope_label VARCHAR(1000) NOT NULL,
    content MEDIUMTEXT NOT NULL,
    source VARCHAR(16) NOT NULL DEFAULT 'ai' CHECK (source IN ('ai', 'api')),
    model VARCHAR(191) NULL,
    created_by BIGINT NULL,
    created_at VARCHAR(32) NOT NULL,
    team_leader_id BIGINT NULL,
    KEY ix_period_reports_range (start_date, end_date),
    KEY ix_period_reports_department (department_id),
    KEY ix_period_reports_employee (employee_id),
    KEY ix_period_reports_team (team_leader_id),
    CONSTRAINT fk_period_department FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    CONSTRAINT fk_period_employee FOREIGN KEY (employee_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_period_creator FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT fk_period_team FOREIGN KEY (team_leader_id) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS sessions (
    id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL PRIMARY KEY,
    user_id BIGINT NULL,
    data MEDIUMTEXT NOT NULL,
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    expires_at VARCHAR(32) NOT NULL,
    KEY ix_sessions_user (user_id),
    KEY ix_sessions_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
