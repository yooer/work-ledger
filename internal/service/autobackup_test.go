package service

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeFTP 测试用的最小 FTP 服务器（内存文件系统，仅支持客户端用到的命令，EPSV 被动模式）。
type fakeFTP struct {
	ln    net.Listener
	mu    sync.Mutex
	files map[string][]byte // 绝对路径 → 内容
	dirs  map[string]bool
	pass  string
}

func newFakeFTP(t *testing.T, pass string) *fakeFTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFTP{ln: ln, files: map[string][]byte{}, dirs: map[string]bool{"/": true}, pass: pass}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeFTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeFTP) names(dir string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for p := range f.files {
		if path.Dir(p) == dir {
			out = append(out, path.Base(p))
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeFTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	reply := func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
	reply("220 fake ftp ready")
	cwd, rnfr := "/", ""
	var dataLn net.Listener
	abs := func(p string) string {
		if strings.HasPrefix(p, "/") {
			return path.Clean(p)
		}
		return path.Clean(path.Join(cwd, p))
	}
	openData := func() (net.Conn, bool) {
		if dataLn == nil {
			reply("425 use EPSV first")
			return nil, false
		}
		reply("150 opening data connection")
		dc, err := dataLn.Accept()
		dataLn.Close()
		dataLn = nil
		return dc, err == nil
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "FEAT":
			reply("211-Features:\r\n EPSV\r\n211 End")
		case "USER":
			reply("331 password please")
		case "PASS":
			if arg != f.pass {
				reply("530 login incorrect")
				continue
			}
			reply("230 logged in")
		case "TYPE", "OPTS":
			reply("200 ok")
		case "CWD":
			p := abs(arg)
			f.mu.Lock()
			ok := f.dirs[p]
			f.mu.Unlock()
			if !ok {
				reply("550 no such directory")
				continue
			}
			cwd = p
			reply("250 ok")
		case "MKD":
			f.mu.Lock()
			f.dirs[abs(arg)] = true
			f.mu.Unlock()
			reply(`257 "` + abs(arg) + `" created`)
		case "EPSV":
			if dataLn, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
				reply("425 cannot open")
				continue
			}
			reply(fmt.Sprintf("229 Entering Extended Passive Mode (|||%d|)", dataLn.Addr().(*net.TCPAddr).Port))
		case "STOR":
			dc, ok := openData()
			if !ok {
				continue
			}
			body, _ := io.ReadAll(dc)
			dc.Close()
			f.mu.Lock()
			f.files[abs(arg)] = body
			f.mu.Unlock()
			reply("226 transfer complete")
		case "NLST":
			dc, ok := openData()
			if !ok {
				continue
			}
			for _, n := range f.names(cwd) {
				fmt.Fprintf(dc, "%s\r\n", n)
			}
			dc.Close()
			reply("226 transfer complete")
		case "RNFR":
			rnfr = abs(arg)
			reply("350 ready for RNTO")
		case "RNTO":
			f.mu.Lock()
			f.files[abs(arg)] = f.files[rnfr]
			delete(f.files, rnfr)
			f.mu.Unlock()
			reply("250 renamed")
		case "DELE":
			f.mu.Lock()
			delete(f.files, abs(arg))
			f.mu.Unlock()
			reply("250 deleted")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

func autoFiles(names []string) []string {
	var out []string
	for _, n := range names {
		if autoBackupName.MatchString(n) {
			out = append(out, n)
		}
	}
	return out
}

func TestAutoBackupUploadAndPrune(t *testing.T) {
	s, _ := newTestServices(t)
	srv := newFakeFTP(t, "ftp-pass")
	dir := s.Core.Cfg.Paths.Backups
	for _, n := range []string{"auto-20261101-020000.json.gz", "auto-20261102-020000.json.gz", "manual-keep.json.gz"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv.files["/bk/sub/auto-20261030-020000.json.gz"] = []byte("old")
	srv.files["/bk/sub/auto-20261031-020000.json.gz"] = []byte("old")
	srv.files["/bk/sub/auto-20261101-020000.json.gz"] = []byte("old")
	srv.files["/bk/sub/other.txt"] = []byte("keep")
	srv.dirs["/bk"] = true // /bk/sub 不存在，应自动创建

	cfg := AutoBackupConfig{Enabled: true, Time: "02:00", KeepLocal: 2, KeepFTP: 2,
		FTP: FTPConfig{Host: "127.0.0.1", Port: srv.port(), User: "backup", Password: "ftp-pass", Dir: "bk/sub/"}}
	if err := s.AutoBackup.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if msg, err := s.AutoBackup.TestFTP(s.AutoBackup.WithStoredPassword(FTPConfig{Host: "127.0.0.1", Port: srv.port(), User: "backup", Dir: "/bk/sub"})); err != nil {
		t.Fatalf("test ftp with stored password: %v", err)
	} else if !strings.Contains(msg, "3 个") {
		t.Fatalf("test ftp message = %q", msg)
	}
	if _, err := s.AutoBackup.TestFTP(FTPConfig{Host: "127.0.0.1", Port: srv.port(), User: "backup", Password: "wrong", Dir: "/"}); err == nil {
		t.Fatal("wrong password should fail")
	}

	st := s.AutoBackup.Run("manual")
	if !st.OK || !st.Uploaded {
		t.Fatalf("run: %+v", st)
	}
	entries, _ := os.ReadDir(dir)
	var local []string
	for _, e := range entries {
		local = append(local, e.Name())
	}
	if a := autoFiles(local); len(a) != 2 || a[1] != st.File || a[0] != "auto-20261102-020000.json.gz" {
		t.Fatalf("local auto files = %v", a)
	}
	if !strings.Contains(strings.Join(local, ","), "manual-keep.json.gz") {
		t.Fatal("non-auto local file removed")
	}
	remote := srv.names("/bk/sub")
	if a := autoFiles(remote); len(a) != 2 || a[1] != st.File {
		t.Fatalf("remote auto files = %v", remote)
	}
	if !strings.Contains(strings.Join(remote, ","), "other.txt") || strings.Contains(strings.Join(remote, ","), ".part") {
		t.Fatalf("remote files = %v", remote)
	}
	// 上传的就是可恢复的备份
	srv.mu.Lock()
	body := srv.files["/bk/sub/"+st.File]
	srv.mu.Unlock()
	if _, err := s.Backup.Decode(body); err != nil {
		t.Fatalf("uploaded backup not restorable: %v", err)
	}
	if last := s.AutoBackup.LastStatus(); last == nil || last.File != st.File {
		t.Fatalf("last status = %+v", last)
	}

	// FTP 不可用：本地备份仍成功，状态记录上传失败
	srv.ln.Close()
	st = s.AutoBackup.Run("manual")
	if !st.OK || st.Uploaded || !strings.Contains(st.Message, "上传 FTP 失败") {
		t.Fatalf("ftp down: %+v", st)
	}
}

func TestAutoBackupTickOncePerDay(t *testing.T) {
	s, _ := newTestServices(t) // 当前时间固定为 12:00
	count := func() int {
		entries, _ := os.ReadDir(s.Core.Cfg.Paths.Backups)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return len(autoFiles(names))
	}
	if err := s.AutoBackup.SaveConfig(AutoBackupConfig{Enabled: false, Time: "02:00", KeepLocal: 7}); err != nil {
		t.Fatal(err)
	}
	s.AutoBackup.Tick(fixedNow)
	if count() != 0 {
		t.Fatal("disabled auto backup ran")
	}
	if err := s.AutoBackup.SaveConfig(AutoBackupConfig{Enabled: true, Time: "13:00", KeepLocal: 7}); err != nil {
		t.Fatal(err)
	}
	s.AutoBackup.Tick(fixedNow)
	if count() != 0 {
		t.Fatal("ran before the scheduled time")
	}
	if err := s.AutoBackup.SaveConfig(AutoBackupConfig{Enabled: true, Time: "02:00", KeepLocal: 7}); err != nil {
		t.Fatal(err)
	}
	s.AutoBackup.Tick(fixedNow)
	s.AutoBackup.Tick(fixedNow)
	if count() != 1 {
		t.Fatalf("auto backups = %d, want 1", count())
	}
	if s.AutoBackup.Config().NextRun != "2026-11-06 02:00" {
		t.Fatalf("next run = %q", s.AutoBackup.Config().NextRun)
	}
}
