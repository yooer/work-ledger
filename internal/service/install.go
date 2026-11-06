package service

import (
	"fmt"
	"regexp"

	"work-ledger/internal/db"
	"work-ledger/internal/password"
	"work-ledger/internal/timeutil"
)

// InstallOptions 安装参数。
type InstallOptions struct {
	AdminUser     string
	AdminPassword string
	Demo          bool
}

// InstallResult 安装输出。
type InstallResult struct {
	Notes             []string
	AdminCreated      bool
	AdminUser         string
	GeneratedPassword string // 自动生成时返回明文，仅打印一次
	DemoPassword      string
}

var adminUserRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{3,64}$`)

// Install 建表/升级、写入默认设置、创建管理员与默认分类，可选演示数据。可重复执行，不会清空数据。
func Install(c *Core, opt InstallOptions) (*InstallResult, error) {
	if opt.AdminUser == "" {
		opt.AdminUser = "admin"
	}
	if !adminUserRe.MatchString(opt.AdminUser) {
		return nil, fmt.Errorf("管理员登录名需为 3-64 位字母、数字或 _ . @ -")
	}
	if opt.AdminPassword != "" && len(opt.AdminPassword) < 8 {
		return nil, fmt.Errorf("管理员密码至少需要 8 位")
	}
	res := &InstallResult{AdminUser: opt.AdminUser}

	notes, err := c.DB.Migrate()
	if err != nil {
		return nil, fmt.Errorf("数据库初始化失败：%w", err)
	}
	res.Notes = append(res.Notes, notes...)
	res.Notes = append(res.Notes, "数据库结构已就绪（含周报、年度/时段总结、2FA、导入去重与会话表）。")

	settings := &Settings{Core: c}
	if err := settings.EnsureDefaults(); err != nil {
		return nil, err
	}

	ts := now()
	count, err := db.Int(c.DB, "SELECT COUNT(*) FROM users")
	if err != nil {
		return nil, err
	}
	if count == 0 {
		plain := opt.AdminPassword
		if plain == "" {
			plain = password.Random(16)
			res.GeneratedPassword = plain
		}
		h, err := password.Hash(plain)
		if err != nil {
			return nil, err
		}
		if _, err := db.Insert(c.DB, `INSERT INTO users (username, password_hash, name, employee_no, role, department_id, phone, email, is_active, created_at, updated_at)
			VALUES (?, ?, '系统管理员', NULL, 'admin', NULL, NULL, NULL, 1, ?, ?)`, opt.AdminUser, h, ts, ts); err != nil {
			return nil, err
		}
		res.AdminCreated = true
		res.Notes = append(res.Notes, "已创建管理员账号："+opt.AdminUser)
	} else {
		res.Notes = append(res.Notes, "用户表已有数据，跳过管理员初始化。")
	}

	seed := []struct {
		name     string
		customer int
		sort     int
	}{{"集客业务", 1, 1}, {"日常维护", 0, 2}, {"内部管理", 0, 3}, {"培训学习", 0, 4}, {"其他工作", 0, 5}}
	catMap := map[string]int64{}
	rows, err := db.All(c.DB, "SELECT id, name FROM work_categories")
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		catMap[r.Str("name")] = r.Int("id")
	}
	for _, s := range seed {
		if _, ok := catMap[s.name]; !ok {
			id, err := db.Insert(c.DB, `INSERT INTO work_categories (name, is_customer_project, is_active, sort_order, created_at, updated_at)
				VALUES (?, ?, 1, ?, ?, ?)`, s.name, s.customer, s.sort, ts, ts)
			if err != nil {
				return nil, err
			}
			catMap[s.name] = id
		}
	}
	if customerID, ok := catMap["集客业务"]; ok {
		for i, name := range []string{"集团专线", "政企云", "视频监控", "物联网", "其他集客项目"} {
			if _, err := db.Exec(c.DB, `INSERT INTO project_categories (work_category_id, name, is_active, sort_order, created_at, updated_at)
				SELECT ?, ?, 1, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM project_categories WHERE work_category_id = ? AND name = ?)`,
				customerID, name, i+1, ts, ts, customerID, name); err != nil {
				return nil, err
			}
		}
	}
	res.Notes = append(res.Notes, "工作分类与子分类已就绪（只增改、不删除）。")

	if opt.Demo {
		demoPwd, err := installDemo(c, opt.AdminUser, catMap)
		if err != nil {
			return nil, err
		}
		if demoPwd != "" {
			res.DemoPassword = demoPwd
			res.Notes = append(res.Notes, "已创建演示部门、人员与 6 条上周台账记录。")
		} else {
			res.Notes = append(res.Notes, "已有部门或人员数据，跳过演示数据。")
		}
	}
	return res, nil
}

func installDemo(c *Core, adminUser string, catMap map[string]int64) (string, error) {
	for _, n := range []string{"manager", "zhangsan", "lisi", "wangwu"} {
		if n == adminUser {
			return "", nil
		}
	}
	users, _ := db.Int(c.DB, "SELECT COUNT(*) FROM users")
	depts, _ := db.Int(c.DB, "SELECT COUNT(*) FROM departments")
	if users > 1 || depts != 0 {
		return "", nil
	}
	demoPwd := password.Random(12)
	ts := now()
	err := c.DB.Tx(func(tx db.Q) error {
		dept := map[string]int64{}
		tops := []struct {
			name     string
			sort     int
			children []string
		}{{"集客部", 10, []string{"集客一组", "集客二组"}}, {"网络运维部", 20, []string{"运维一组"}}, {"综合管理部", 30, nil}}
		for _, t := range tops {
			id, err := db.Insert(tx, "INSERT INTO departments (name, parent_id, is_active, sort_order, created_at, updated_at) VALUES (?, NULL, 1, ?, ?, ?)", t.name, t.sort, ts, ts)
			if err != nil {
				return err
			}
			dept[t.name] = id
			for i, ch := range t.children {
				cid, err := db.Insert(tx, "INSERT INTO departments (name, parent_id, is_active, sort_order, created_at, updated_at) VALUES (?, ?, 1, ?, ?, ?)", ch, id, (i+1)*10, ts, ts)
				if err != nil {
					return err
				}
				dept[ch] = cid
			}
		}
		adminID, _ := db.Int(tx, "SELECT id FROM users ORDER BY id LIMIT 1")
		if _, err := db.Exec(tx, "UPDATE users SET department_id = ?, updated_at = ? WHERE id = ?", dept["综合管理部"], ts, adminID); err != nil {
			return err
		}
		h, err := password.Hash(demoPwd)
		if err != nil {
			return err
		}
		uid := map[string]int64{}
		for _, u := range []struct{ user, name, no, role, dept string }{
			{"manager", "集客部经理", "M0001", "manager", "集客部"},
			{"zhangsan", "张三", "E1001", "employee", "集客一组"},
			{"lisi", "李四", "E1002", "employee", "集客二组"},
			{"wangwu", "王五", "E1003", "employee", "运维一组"},
		} {
			id, err := db.Insert(tx, `INSERT INTO users (username, password_hash, name, employee_no, role, department_id, phone, email, is_active, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, 1, ?, ?)`, u.user, h, u.name, u.no, u.role, dept[u.dept], ts, ts)
			if err != nil {
				return err
			}
			uid[u.user] = id
		}
		proj := map[string]int64{}
		prow, _ := db.All(tx, "SELECT id, name FROM project_categories")
		for _, r := range prow {
			proj[r.Str("name")] = r.Int("id")
		}
		creatorDept := map[string]string{"wangwu": "运维一组", "manager": "集客部", "lisi": "集客二组", "zhangsan": "集客一组"}
		monday := timeutil.MondayOf(timeutil.AddDays(timeutil.Today(), -7))
		seeds := []struct {
			cat, proj, content, at string
			diff, count            int
			creator                string
			parts                  []string
		}{
			{"集客业务", "集团专线", "完成某集团客户 100M 专线开通与业务验收，客户侧设备联调正常。", monday + " 09:30:00", 4, 1, "zhangsan", []string{"zhangsan", "lisi"}},
			{"集客业务", "政企云", "为政企客户扩容云主机 2 台并完成数据迁移，业务恢复验证通过。", timeutil.AddDays(monday, 1) + " 14:00:00", 3, 2, "lisi", []string{"lisi", "zhangsan"}},
			{"日常维护", "", "核心机房例行巡检，处理 1 处交换机端口告警。", timeutil.AddDays(monday, 2) + " 10:00:00", 2, 1, "wangwu", []string{"wangwu"}},
			{"内部管理", "", "参加部门周例会，汇报集客项目进度并同步本周资源安排。", monday + " 16:00:00", 1, 1, "manager", []string{"manager"}},
			{"培训学习", "", "参加集客产品与解决方案培训，完成随堂测试。", timeutil.AddDays(monday, 3) + " 15:00:00", 2, 1, "zhangsan", []string{"zhangsan", "wangwu"}},
			{"集客业务", "视频监控", "处理客户视频监控平台离线告警，更换故障电源并恢复全部点位。", timeutil.AddDays(monday, 4) + " 11:20:00", 5, 2, "lisi", []string{"lisi", "wangwu"}},
		}
		for _, s := range seeds {
			var pid any
			if s.proj != "" {
				pid = proj[s.proj]
			}
			rid, err := db.Insert(tx, `INSERT INTO work_records (work_category_id, project_category_id, content, work_at, difficulty, work_count, department_id, created_by, created_at, updated_by, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, catMap[s.cat], pid, s.content, s.at, s.diff, s.count, dept[creatorDept[s.creator]], uid[s.creator], ts, uid[s.creator], ts)
			if err != nil {
				return err
			}
			for _, p := range s.parts {
				if _, err := db.Exec(tx, "INSERT OR IGNORE INTO work_record_participants (work_record_id, user_id) VALUES (?, ?)", rid, uid[p]); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return demoPwd, nil
}
