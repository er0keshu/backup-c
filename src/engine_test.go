package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 构造源树,返回 src 根。
func buildTree(t *testing.T, src string) {
	t.Helper()
	mk := func(rel, content string) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkdir := func(rel string) {
		if err := os.MkdirAll(filepath.Join(src, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk(".zcode/v2/provider_config.json", `{"a":1}`)
	mk(".zcode/v2/cache/big.tmp", "CACHE")
	mk(".zcode/logs/app.log", "LOG")
	mk(".ssh/id_rsa", "KEY")
	mk(".ssh/keys/sub/key2", "KEY2")
	mk("notes/keep.txt", "KEEP")
	mk("notes/drop.log", "DROPLOG")
	mk("node_modules/pkg/index.js", "NM")
	mk("deep/a/b/node_modules/x/y.js", "NESTED_NM")
	mkdir("structure/empty/deep")
	mkdir("structure/also-empty")
}

// snapshot 记录源树所有文件的 名称:大小:修改时间。
func snapshot(t *testing.T, src string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		b.WriteString(rel + ":" + info.ModTime().String() + ":" + string(rune(info.Size())) + ";")
		return nil
	})
	return b.String()
}

// makeEngine 在 appDir 写 config.yaml 并构造引擎。
// cfgTarget 非空则写入 config.targetDir;cliTarget 非空则模拟 -d 参数。
func makeEngine(t *testing.T, appDir, src string, interval int, include, exclude []string) *Engine {
	t.Helper()
	e, _ := makeEngineOpts(t, appDir, src, interval, "", "", include, exclude)
	return e
}

// makeEngineOpts 完整版:可指定 cfgTarget(写入 config) 与 cliTarget(模拟 -d)。
func makeEngineOpts(t *testing.T, appDir, src string, interval int, cfgTarget, cliTarget string, include, exclude []string) (*Engine, string) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("sourceRoot: " + quoteYaml(src) + "\n")
	if cfgTarget != "" {
		sb.WriteString("targetDir: " + quoteYaml(cfgTarget) + "\n")
	}
	if interval > 0 {
		sb.WriteString(fmt.Sprintf("interval: %d\n", interval))
	}
	sb.WriteString("include:\n")
	for _, i := range include {
		sb.WriteString("  - " + quoteYaml(i) + "\n")
	}
	if len(exclude) == 0 {
		sb.WriteString("exclude: []\n")
	} else {
		sb.WriteString("exclude:\n")
		for _, e := range exclude {
			sb.WriteString("  - " + quoteYaml(e) + "\n")
		}
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := newEngine(appDir, cliTarget, nil)
	if err != nil {
		t.Fatalf("newEngine: %v", err)
	}
	return e, e.DestRoots[0]
}

func quoteYaml(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}

// TestExcludePriority 验证 include/exclude 语义与镜像增删、空目录、源只读。
func TestExcludePriority(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	app := filepath.Join(root, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	buildTree(t, src)

	inc := []string{".zcode", ".ssh", "notes", "node_modules", "deep", "structure"}
	exc := []string{"*.log", "node_modules", ".zcode/v2/cache/**"}
	e := makeEngine(t, app, src, 10, inc, exc)

	before := snapshot(t, src)
	st, err := e.RunOnce()
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	t.Logf("第1次: %+v", st)

	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(e.DestRoots[0], filepath.FromSlash(rel)))
		return err == nil
	}
	check := func(name string, cond bool) {
		if cond {
			t.Logf("PASS  %s", name)
		} else {
			t.Errorf("FAIL  %s", name)
		}
	}

	check("provider_config.json 已备份", exists(".zcode/v2/provider_config.json"))
	check("cache 子树被排除", !exists(".zcode/v2/cache/big.tmp"))
	check("app.log 被排除", !exists(".zcode/logs/app.log"))
	check("keep.txt 已备份", exists("notes/keep.txt"))
	check("drop.log 被排除", !exists("notes/drop.log"))
	check("key2 已备份", exists(".ssh/keys/sub/key2"))
	check("node_modules 顶层被排除", !exists("node_modules/pkg/index.js"))
	check("嵌套 node_modules 被排除", !exists("deep/a/b/node_modules/x/y.js"))
	check("空目录 structure/empty/deep 保留", exists("structure/empty/deep"))
	check("空目录 structure/also-empty 保留", exists("structure/also-empty"))
	check("源未被改动", before == snapshot(t, src))

	// 第 2 次:删文件、删空目录、改内容 -> 镜像应同步
	if err := os.Remove(filepath.Join(src, "notes", "keep.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(src, "structure", "also-empty")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".ssh", "id_rsa"), []byte("KEY-CHANGED"), 0o644); err != nil {
		t.Fatal(err)
	}
	before2 := snapshot(t, src)
	st2, err := e.RunOnce()
	if err != nil {
		t.Fatalf("RunOnce 2: %v", err)
	}
	t.Logf("第2次: %+v", st2)
	check("源删除的 keep.txt 已从 data 清除", !exists("notes/keep.txt"))
	check("源删除的空目录已从 data 清除", !exists("structure/also-empty"))
	check("仍在源中的空目录保留", exists("structure/empty/deep"))
	got, _ := os.ReadFile(filepath.Join(e.DestRoots[0], ".ssh", "id_rsa"))
	check("id_rsa 内容已更新", string(got) == "KEY-CHANGED")
	check("二次源仍未被改动", before2 == snapshot(t, src))

	// 第 3 次:新增 include 目录 -> 增量新增,且新目录里的 *.log 仍被排除
	if err := os.MkdirAll(filepath.Join(src, "extra"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "extra", "new1.txt"), []byte("N1"), 0o644)
	os.WriteFile(filepath.Join(src, "extra", "new2.log"), []byte("N2"), 0o644)
	e2 := makeEngine(t, app, src, 10, append(inc, "extra"), exc)
	if _, err := e2.RunOnce(); err != nil {
		t.Fatalf("RunOnce 3: %v", err)
	}
	check("新增 extra/new1.txt 已备份", exists("extra/new1.txt"))
	check("新增 extra/new2.log 仍被排除", !exists("extra/new2.log"))
}

// TestMultiTarget 验证一次备份同时镜像到多个目标目录,且镜像删除对每个目标都生效。
func TestMultiTarget(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	app := filepath.Join(root, "app")
	os.MkdirAll(app, 0o755)
	buildTree(t, src)

	d1 := filepath.Join(root, "out1")
	d2 := filepath.Join(root, "out2")
	inc := []string{".ssh", "notes"}
	exc := []string{"*.log"}
	// 模拟 -d "out1,out2"
	e := makeEngineWithTargets(t, app, src, 10, d1+","+d2, inc, exc)

	if len(e.DestRoots) != 2 {
		t.Fatalf("期望 2 个目标, 实际 %d: %v", len(e.DestRoots), e.DestRoots)
	}
	if _, err := e.RunOnce(); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	checkIn := func(dir, rel string) bool {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		return err == nil
	}
	for _, d := range []string{d1, d2} {
		if !checkIn(d, ".ssh/id_rsa") || !checkIn(d, "notes/keep.txt") {
			t.Errorf("目标 %s 未包含预期文件", d)
		}
		if checkIn(d, "notes/drop.log") {
			t.Errorf("目标 %s 未排除 *.log", d)
		}
	}
	t.Logf("PASS  两个目标都收到备份且排除生效")

	// 源删除文件 -> 两个目标的镜像都应清除
	os.Remove(filepath.Join(src, "notes", "keep.txt"))
	if _, err := e.RunOnce(); err != nil {
		t.Fatalf("RunOnce 2: %v", err)
	}
	for _, d := range []string{d1, d2} {
		if checkIn(d, "notes/keep.txt") {
			t.Errorf("目标 %s 未清除源已删除的文件", d)
		}
	}
	t.Logf("PASS  两个目标都同步了删除")
}

// TestDedupeTargets 验证重复目标会被去重。
func TestDedupeTargets(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	app := filepath.Join(root, "app")
	os.MkdirAll(app, 0o755)
	buildTree(t, src)
	d := filepath.Join(root, "out")
	e := makeEngineWithTargets(t, app, src, 10, d+","+d+","+d, []string{"notes"}, nil)
	if len(e.DestRoots) != 1 {
		t.Errorf("重复目标未去重, 实际 %v", e.DestRoots)
	} else {
		t.Logf("PASS  重复目标已去重 => %v", e.DestRoots)
	}
}

// makeEngineWithTargets 直接用 -d 指定的多目标串构造引擎。
func makeEngineWithTargets(t *testing.T, appDir, src string, interval int, cliTargets string, include, exclude []string) *Engine {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("sourceRoot: " + quoteYaml(src) + "\n")
	if interval > 0 {
		sb.WriteString(fmt.Sprintf("interval: %d\n", interval))
	}
	sb.WriteString("include:\n")
	for _, i := range include {
		sb.WriteString("  - " + quoteYaml(i) + "\n")
	}
	if len(exclude) == 0 {
		sb.WriteString("exclude: []\n")
	} else {
		sb.WriteString("exclude:\n")
		for _, e := range exclude {
			sb.WriteString("  - " + quoteYaml(e) + "\n")
		}
	}
	if err := os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := newEngine(appDir, cliTargets, nil)
	if err != nil {
		t.Fatalf("newEngine: %v", err)
	}
	return e
}

// TestSafety 验证安全约束:.. 与绝对路径被拒。
func TestSafety(t *testing.T) {
	if _, err := safeRel("../etc/passwd"); err == nil {
		t.Errorf(".. 应被拒绝")
	}
	if _, err := safeRel("C:/Windows"); err == nil {
		t.Errorf("绝对路径应被拒绝")
	}
	if got, err := safeRel(".zcode/./v2"); err != nil || got != ".zcode/v2" {
		t.Errorf("规范化失败: %q %v", got, err)
	}
}

// TestResolveInterval 验证间隔优先级: -i -> config.interval -> 默认 10。
func TestResolveInterval(t *testing.T) {
	cases := []struct {
		name        string
		cliInterval int
		cfgInterval int
		want        int
		wantSrc     string
	}{
		{"-i 优先于 config.interval", 20, 5, 20, "-i 参数"},
		{"无 -i 时用 config.interval", 0, 30, 30, "config.interval"},
		{"都没有则默认 10 分钟", 0, 0, 10, "默认"},
		{"interval 大于 59 也成立", 0, 90, 90, "config.interval"},
	}
	for _, c := range cases {
		got, src := resolveInterval(c.cliInterval, c.cfgInterval)
		if got != c.want || src != c.wantSrc {
			t.Errorf("%s: 期望 (%d,%q), 实际 (%d,%q)", c.name, c.want, c.wantSrc, got, src)
		} else {
			t.Logf("PASS  %s => %d (%s)", c.name, got, src)
		}
	}
}

// TestParseArgs 验证 -i/-d 参数解析(位置无关、两种写法、非法值)。
func TestParseArgs(t *testing.T) {
	cases := []struct {
		args     []string
		action   string
		interval int
		target   string
		wantErr  bool
	}{
		{[]string{"install", "-i", "20"}, "install", 20, "", false},
		{[]string{"-i", "20", "install"}, "install", 20, "", false},
		{[]string{"run", "--interval=15"}, "run", 15, "", false},
		{[]string{"-i=7"}, "run", 7, "", false},
		{[]string{"-i", "25"}, "run", 25, "", false},
		{[]string{"once"}, "once", 0, "", false},
		{[]string{}, "run", 0, "", false},
		{[]string{"-d", `D:\bk`}, "run", 0, `D:\bk`, false},
		{[]string{"run", "-d", `D:\bk`, "-i", "5"}, "run", 5, `D:\bk`, false},
		{[]string{"--target=E:\\out"}, "run", 0, `E:\out`, false},
		{[]string{"-d=" + `C:\x y`}, "run", 0, `C:\x y`, false},
		{[]string{"-d", `D:\a,E:\b`}, "run", 0, `D:\a,E:\b`, false}, // 多目标(逗号)
		{[]string{"-i", "0"}, "", 0, "", true},
		{[]string{"-i", "abc"}, "", 0, "", true},
		{[]string{"-i"}, "", 0, "", true},
		{[]string{"-d"}, "", 0, "", true},
	}
	for _, c := range cases {
		action, interval, target, err := parseArgs(c.args)
		if c.wantErr {
			if err == nil {
				t.Errorf("args=%v 期望报错,却成功", c.args)
			} else {
				t.Logf("PASS  args=%v 正确报错: %v", c.args, err)
			}
			continue
		}
		if err != nil || action != c.action || interval != c.interval || target != c.target {
			t.Errorf("args=%v 期望 (%q,%d,%q), 实际 (%q,%d,%q) err=%v",
				c.args, c.action, c.interval, c.target, action, interval, target, err)
		} else {
			t.Logf("PASS  args=%v => action=%q interval=%d target=%q", c.args, action, interval, target)
		}
	}
}

// TestResolveTarget 验证目标目录优先级与多目标合并: -d -> config.targetDir -> 默认。
func TestResolveTarget(t *testing.T) {
	cases := []struct {
		name    string
		cli     string
		cfg     StringList
		want    string
		wantSrc string
	}{
		{"-d 优先于 config.targetDir", `D:\d1`, StringList{`D:\cfg`}, `D:\d1`, "-d 参数"},
		{"无 -d 时用 config.targetDir", "", StringList{`D:\cfg`}, `D:\cfg`, "config.targetDir"},
		{"都没有则用默认", "", nil, `<exe>\dist`, "默认"},
		{"空白 -d 视作未指定", "   ", StringList{`D:\cfg`}, `D:\cfg`, "config.targetDir"},
		{"config 多目标用逗号拼接", "", StringList{`D:\a`, `E:\b`}, `D:\a,E:\b`, "config.targetDir"},
	}
	for _, c := range cases {
		got, src := resolveTarget(c.cli, c.cfg, `<exe>\dist`)
		if got != c.want || src != c.wantSrc {
			t.Errorf("%s: 期望 (%q,%q), 实际 (%q,%q)", c.name, c.want, c.wantSrc, got, src)
		} else {
			t.Logf("PASS  %s => %q (%s)", c.name, got, src)
		}
	}
}

// TestTargetDirUnmarshal 验证 config.targetDir 的两种 YAML 写法都能被
// StringList.UnmarshalYAML 正确解析(标量逗号 / 序列),并合并成全角/半角通吃的列表。
func TestTargetDirUnmarshal(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	buildTree(t, src)
	d1 := filepath.Join(root, "a")
	d2 := filepath.Join(root, "b")
	d3 := filepath.Join(root, "c")

	cases := []struct {
		name string
		body string // targetDir 那段 YAML
		want []string
	}{
		{"标量逗号分隔", "targetDir: " + quoteYaml(d1+","+d2) + "\n", []string{d1, d2}},
		{"序列", "targetDir:\n  - " + quoteYaml(d1) + "\n  - " + quoteYaml(d2) + "\n", []string{d1, d2}},
		{"序列含逗号项", "targetDir:\n  - " + quoteYaml(d1+","+d2) + "\n  - " + quoteYaml(d3) + "\n",
			[]string{d1, d2, d3}},
	}
	for _, c := range cases {
		app := filepath.Join(root, "app-"+c.name)
		os.MkdirAll(app, 0o755)
		cfg := "sourceRoot: " + quoteYaml(src) + "\n" + c.body + "include:\n  - notes\n"
		if err := os.WriteFile(filepath.Join(app, "config.yaml"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		e, err := newEngine(app, "", nil)
		if err != nil {
			t.Fatalf("%s: newEngine: %v", c.name, err)
		}
		if fmt.Sprint(e.DestRoots) != fmt.Sprint(c.want) {
			t.Errorf("%s: 期望 %v, 实际 %v", c.name, c.want, e.DestRoots)
		} else {
			t.Logf("PASS  %s => %v", c.name, e.DestRoots)
		}
	}
}

// TestSplitList 验证逗号分隔(含全角逗号、空白、空项)的切分。
func TestSplitList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`D:\a,E:\b`, []string{`D:\a`, `E:\b`}},
		{`D:\a, E:\b ,`, []string{`D:\a`, `E:\b`}},
		{`D:\a，E:\b`, []string{`D:\a`, `E:\b`}}, // 全角逗号
		{`C:\x y`, []string{`C:\x y`}},          // 路径含空格,不去内部空白
		{"", nil},
	}
	for _, c := range cases {
		got := splitList(c.in)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("splitList(%q) 期望 %v, 实际 %v", c.in, c.want, got)
		} else {
			t.Logf("PASS  splitList(%q) => %v", c.in, got)
		}
	}
}

// TestConfigSignature 验证配置指纹:同内容稳定,改内容/改存在性会变化。
// 关键回归:内容变了但长度与 mtime 都没变(同步盘/cp -p 会保留 mtime)时,
// 指纹仍必须变化,否则热重载会永远不触发。
func TestConfigSignature(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")

	if configSignature(dir) != "" {
		t.Errorf("文件不存在时应返回空串")
	}
	os.WriteFile(cfg, []byte("interval: 10\n"), 0o644)
	s1 := configSignature(dir)
	if s1 == "" {
		t.Fatal("文件存在时不应为空")
	}
	if configSignature(dir) != s1 {
		t.Errorf("同内容应稳定不变")
	}
	os.WriteFile(cfg, []byte("interval: 20\n"), 0o644)
	s2 := configSignature(dir)
	if s2 == s1 {
		t.Errorf("内容变化后指纹应变化: %q vs %q", s1, s2)
	}

	// 回归:等长内容 + 还原 mtime,指纹仍须变化。
	fi, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := fi.ModTime()
	if err := os.WriteFile(cfg, []byte("interval: 21\n"), 0o644); err != nil { // 与 20 等长
		t.Fatal(err)
	}
	if err := os.Chtimes(cfg, old, old); err != nil { // 还原 mtime
		t.Fatal(err)
	}
	s3 := configSignature(dir)
	if s3 == s2 {
		t.Errorf("等长内容 + 相同 mtime 时指纹仍应变化(否则重载失效): %q", s3)
	} else {
		t.Logf("PASS  等长+还原 mtime 也能检出变化: %q -> %q", s2, s3)
	}

	os.Remove(cfg)
	if configSignature(dir) != "" {
		t.Errorf("文件删除后应返回空串")
	}
	t.Logf("PASS  指纹变化检测正常: %q -> %q", s1, s2)
}
