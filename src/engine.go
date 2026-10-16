// 备份引擎:把 config.yaml 里 include 的文件/目录镜像同步到 data/ 下,
// exclude 优先级更高(命中文件跳过,命中目录则整个子树跳过)。
// 以源数据为准:data/ 是 include 减 exclude 的完整镜像 —— 源文件结构(含空目录)与
// 数据都被忠实复制,源里删除/改名的东西下一次备份会从 data/ 清除。
// 源文件全程只读,绝不修改。
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// Manifest 对应 config.yaml。
type Manifest struct {
	SourceRoot string     `yaml:"sourceRoot"`
	TargetDir  StringList `yaml:"targetDir"` // 备份目标目录(可多个;优先级低于 -d 参数)
	Interval   int        `yaml:"interval"`  // 备份间隔(分钟);优先级低于 -i 参数
	Include    []string   `yaml:"include"`
	Exclude    []string   `yaml:"exclude"`
}

// StringList 支持两种 YAML 写法:
//
//	targetDir: "D:\\a,D:\\b"      # 标量,逗号分隔
//	targetDir: ["D:\\a", "D:\\b"] # 序列
type StringList []string

func (s *StringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var v string
		if err := node.Decode(&v); err != nil {
			return err
		}
		*s = splitList(v)
	case yaml.SequenceNode:
		var vs []string
		if err := node.Decode(&vs); err != nil {
			return err
		}
		var out []string
		for _, v := range vs {
			out = append(out, splitList(v)...)
		}
		*s = out
	}
	return nil
}

// splitList 按半角/全角逗号切分并去空白(空项丢弃)。
func splitList(s string) []string {
	var out []string
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' }) {
		if t := strings.TrimSpace(seg); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Stats 单次备份的统计。
type Stats struct {
	Copied      int
	Updated     int
	Skipped     int
	Deleted     int
	DirsMade    int
	DirsRemoved int
}

// Engine 一次备份运行的上下文。
type Engine struct {
	Home      string   // 源根目录(绝对路径)
	DestRoots []string // 备份目标根目录列表(绝对路径,至少一个)
	Manifest  Manifest
	Logger    *Logger
}

// posix 把本地分隔符统一成 /。
func posix(p string) string { return filepath.ToSlash(p) }

// isGlob 判断是否含通配符。
func isGlob(p string) bool { return strings.ContainsAny(p, "?*{}[]") }

// safeRel 校验清单路径:必须是相对源根的普通路径,禁止绝对路径与 ..
func safeRel(rule string) (string, error) {
	p := strings.ReplaceAll(rule, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':') {
		return "", fmt.Errorf("清单路径必须是相对 sourceRoot 的路径: %s", rule)
	}
	parts := strings.Split(p, "/")
	for _, seg := range parts {
		if seg == ".." {
			return "", fmt.Errorf("清单路径不能包含 .. : %s", rule)
		}
	}
	var keep []string
	for _, seg := range parts {
		if seg != "" && seg != "." { // 归一化空段与 "."
			keep = append(keep, seg)
		}
	}
	return strings.Join(keep, "/"), nil
}

// isExcluded 判断相对源根的 posix 路径是否被排除。
//
//	含 / 的规则按整路径 glob,并检查各级祖先目录(命中目录 => 整子树排除)
//	不含 / 的规则按"任意层级的名字"匹配(如 node_modules、*.log)
func isExcluded(rel string, rules []string) bool {
	parts := strings.Split(rel, "/")
	for _, rule := range rules {
		r := strings.TrimSuffix(strings.ReplaceAll(rule, "\\", "/"), "/")
		r = strings.TrimPrefix(r, "./")
		if r == "" {
			continue
		}
		if strings.Contains(r, "/") {
			if ok, _ := doublestar.Match(r, rel); ok {
				return true
			}
			for i := 1; i < len(parts); i++ {
				if ok, _ := doublestar.Match(r, strings.Join(parts[:i], "/")); ok {
					return true
				}
			}
		} else {
			for _, seg := range parts {
				if ok, _ := doublestar.Match(r, seg); ok {
					return true
				}
			}
		}
	}
	return false
}

// newEngine 读清单,校验源根,给出引擎实例。
// cliTarget 来自 -d 参数(空表示未指定),优先级高于 config.targetDir。
func newEngine(exeDir, cliTarget string, logger *Logger) (*Engine, error) {
	cfgPath := filepath.Join(exeDir, "config.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("读取 config.yaml 失败: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("解析 config.yaml 失败: %w", err)
	}

	home := m.SourceRoot
	if home == "" {
		// 回退:仅当未显式配置时才用用户主目录。计划任务/服务账户的主目录
		// 可能与你不同,故生产环境应在 config.yaml 显式指定 sourceRoot。
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("未配置 sourceRoot 且无法取得用户主目录: %w", err)
		}
		home = h
		if logger != nil {
			logger.Printf("[warn] 未配置 sourceRoot,回退到用户主目录 %s(建议显式配置)", home)
		}
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("sourceRoot 非法: %w", err)
	}
	st, err := os.Stat(home)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("sourceRoot 不存在或不是目录: %s", home)
	}

	// 目标目录优先级: -d 参数 -> config.targetDir -> 默认 <exe>/dist
	// 均支持逗号分隔的多个目录。
	rawTargets, destSrc := resolveTarget(cliTarget, m.TargetDir, filepath.Join(exeDir, "dist"))
	var dests []string
	seenDest := map[string]bool{}
	for _, t := range splitList(rawTargets) {
		abs, err := filepath.Abs(t)
		if err != nil {
			return nil, fmt.Errorf("目标目录非法 %q: %w", t, err)
		}
		key := strings.ToLower(abs) // Windows 路径大小写不敏感,去重
		if seenDest[key] {
			continue
		}
		seenDest[key] = true
		dests = append(dests, abs)
	}
	if len(dests) == 0 {
		return nil, fmt.Errorf("未解析出任何有效目标目录")
	}
	if logger != nil {
		logger.Printf("目标目录(%d 个, 来源: %s): %s", len(dests), destSrc, strings.Join(dests, " ; "))
	}

	return &Engine{
		Home:      home,
		DestRoots: dests,
		Manifest:  m,
		Logger:    logger,
	}, nil
}

// resolveTarget 按 -d 参数 -> config.targetDir -> 默认 的优先级给出目标目录串与来源。
// 返回的串可能含逗号分隔的多个目录。
func resolveTarget(cliTarget string, cfgTarget StringList, defaultDir string) (dirs, src string) {
	switch {
	case strings.TrimSpace(cliTarget) != "":
		return cliTarget, "-d 参数"
	case len(cfgTarget) > 0:
		return strings.Join(cfgTarget, ","), "config.targetDir"
	default:
		return defaultDir, "默认"
	}
}

func (e *Engine) warn(format string, a ...any) {
	if e.Logger != nil {
		e.Logger.Printf("[warn] "+format, a...)
	}
}

func (e *Engine) logf(format string, a ...any) {
	if e.Logger != nil {
		e.Logger.Printf(format, a...)
	}
}

// collectFile 记录单个文件(应用 exclude)。
func (e *Engine) collectFile(abs string, files map[string]string) {
	if e.isInsideDest(abs) {
		return // 目标目录内的文件不参与备份
	}
	rel, err := filepath.Rel(e.Home, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		e.warn("跳过源根之外的路径: %s", abs)
		return
	}
	rp := posix(rel)
	if isExcluded(rp, e.Manifest.Exclude) {
		return // exclude 优先
	}
	files[rp] = abs
}

// isInsideDest 判断 abs 是否位于任一备份目标目录内(相等或为其子路径)。
func (e *Engine) isInsideDest(abs string) bool {
	for _, dest := range e.DestRoots {
		rel, err := filepath.Rel(dest, abs)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
			return true
		}
	}
	return false
}

// walkDir 递归遍历目录,收集文件与目录;exclude 命中目录 => 整棵子树剪掉。
func (e *Engine) walkDir(abs string, files map[string]string, dirs map[string]bool, seen map[string]bool) {
	if e.isInsideDest(abs) { // 目标目录落在源内时,跳过它,避免备份自己
		e.warn("跳过备份目标目录自身: %s", abs)
		return
	}
	rel, err := filepath.Rel(e.Home, abs)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return
	}
	rp := posix(rel)
	if isExcluded(rp, e.Manifest.Exclude) {
		return
	}
	real, err := filepath.EvalSymlinks(abs) // 用真实路径防符号链接环
	if err != nil {
		real = abs
	}
	if seen[real] {
		return
	}
	seen[real] = true
	dirs[rp] = true

	entries, err := os.ReadDir(abs)
	if err != nil {
		e.warn("读取目录失败,跳过: %s (%v)", abs, err)
		return
	}
	for _, ent := range entries {
		child := filepath.Join(abs, ent.Name())
		info, err := os.Stat(child)
		if err != nil {
			e.warn("stat 失败,跳过: %s (%v)", child, err)
			continue
		}
		if info.IsDir() {
			e.walkDir(child, files, dirs, seen)
		} else if info.Mode().IsRegular() {
			e.collectFile(child, files)
		}
	}
}

// addAncestors 把 rel 的所有祖先目录登记进 dirs(空目录/结构由此得以保留)。
func addAncestors(rel string, dirs map[string]bool) {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		dirs[strings.Join(parts[:i], "/")] = true
	}
}

// buildTargets 计算本次要备份的集合: files(rel->源绝对路径) 与 dirs(rel,含空目录)。
func (e *Engine) buildTargets() (map[string]string, map[string]bool, error) {
	files := map[string]string{}
	dirs := map[string]bool{}
	seen := map[string]bool{}

	handle := func(rule string) error {
		r, err := safeRel(rule)
		if err != nil {
			return err
		}
		if isGlob(r) {
			matches, gerr := doublestar.FilepathGlob(filepath.Join(e.Home, r))
			if gerr != nil {
				e.warn("glob 展开失败,跳过 %s: %v", rule, gerr)
				return nil
			}
			if len(matches) == 0 {
				e.warn("无匹配,跳过: %s", rule)
			}
			for _, abs := range matches {
				if e.isInsideDest(abs) {
					continue // 跳过备份目标自身
				}
				info, err := os.Stat(abs)
				if err != nil {
					continue
				}
				if info.IsDir() {
					e.walkDir(abs, files, dirs, seen)
				} else if info.Mode().IsRegular() {
					e.collectFile(abs, files)
				}
			}
			return nil
		}
		abs := filepath.Join(e.Home, filepath.FromSlash(r))
		info, err := os.Stat(abs)
		if err != nil {
			e.warn("源不存在,跳过: %s", r)
			return nil
		}
		if info.IsDir() {
			e.walkDir(abs, files, dirs, seen)
		} else {
			e.collectFile(abs, files)
		}
		return nil
	}

	for _, rule := range e.Manifest.Include {
		if err := handle(rule); err != nil {
			return nil, nil, err
		}
	}
	for rel := range files {
		addAncestors(rel, dirs)
	}
	// 安全:源不能落在任一备份目标内
	for _, abs := range files {
		for _, dest := range e.DestRoots {
			rel, err := filepath.Rel(dest, abs)
			if err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
				return nil, nil, fmt.Errorf("源位于备份目标内,拒绝: %s", abs)
			}
		}
	}
	return files, dirs, nil
}

// walkDest 递归列出 DestRoot 下所有文件与目录。
func walkDest(dir string, files *[]string, dirs *[]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		full := filepath.Join(dir, ent.Name())
		if ent.IsDir() {
			*dirs = append(*dirs, full)
			walkDest(full, files, dirs)
		} else {
			*files = append(*files, full)
		}
	}
}

// sameFile 判断目标是否已是源的最新副本(size+mtime)。
func sameFile(srcInfo, dstInfo os.FileInfo) bool {
	if srcInfo.Size() != dstInfo.Size() {
		return false
	}
	d := srcInfo.ModTime().Sub(dstInfo.ModTime())
	if d < 0 {
		d = -d
	}
	return d < 2*time.Second
}

// backupAndLog 执行一次备份并写日志(供 run 循环与启动使用)。
func (e *Engine) backupAndLog(tag string) {
	t0 := time.Now()
	st, err := e.RunOnce()
	if err != nil {
		e.logf("[error] %s 备份失败: %v", tag, err)
		return
	}
	e.logf("%s 备份完成: 新增 %d 更新 %d 未变 %d 删除 %d 空目录复原 %d 清理空目录 %d (%s)",
		tag, st.Copied, st.Updated, st.Skipped, st.Deleted, st.DirsMade, st.DirsRemoved,
		time.Since(t0).Round(time.Millisecond))
}

// RunOnce 执行一次备份:计算一次源集合,再镜像同步到每一个目标目录。
func (e *Engine) RunOnce() (Stats, error) {
	var total Stats
	wanted, wantedDirs, err := e.buildTargets()
	if err != nil {
		return total, err
	}

	for _, dest := range e.DestRoots {
		st, err := e.syncTo(dest, wanted, wantedDirs)
		total.Copied += st.Copied
		total.Updated += st.Updated
		total.Skipped += st.Skipped
		total.Deleted += st.Deleted
		total.DirsMade += st.DirsMade
		total.DirsRemoved += st.DirsRemoved
		if err != nil {
			return total, fmt.Errorf("目标 %s: %w", dest, err)
		}
	}
	return total, nil
}

// syncTo 把已算好的源集合镜像同步到单个目标目录 dest。
func (e *Engine) syncTo(dest string, wanted map[string]string, wantedDirs map[string]bool) (Stats, error) {
	var st Stats

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return st, fmt.Errorf("创建备份目录失败: %w", err)
	}

	// 1) 先建齐所有需要的目录(含空目录 => 完整保留源文件结构)
	for rel := range wantedDirs {
		abs := filepath.Join(dest, filepath.FromSlash(rel))
		if _, err := os.Stat(abs); err != nil {
			if err := os.MkdirAll(abs, 0o755); err != nil {
				return st, fmt.Errorf("创建目录失败 %s: %w", abs, err)
			}
			st.DirsMade++
		}
	}

	// 2) 复制/更新文件(先写后删,复制失败不会丢已有备份)
	for rel, src := range wanted {
		dst := filepath.Join(dest, filepath.FromSlash(rel))
		si, err := os.Stat(src)
		if err != nil {
			e.warn("读取源失败,跳过: %s (%v)", src, err)
			continue
		}
		existed := false
		if di, err := os.Stat(dst); err == nil && di.Mode().IsRegular() {
			existed = true
			if sameFile(si, di) {
				st.Skipped++
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return st, fmt.Errorf("创建目录失败 %s: %w", filepath.Dir(dst), err)
		}
		if err := copyFile(src, dst, si); err != nil {
			return st, fmt.Errorf("复制失败 %s -> %s: %w", src, dst, err)
		}
		if existed {
			st.Updated++
		} else {
			st.Copied++
		}
	}

	// 3) 镜像清理:删除不在集合中的文件,再自底向上删除多余空目录
	var dFiles, dDirs []string
	walkDest(dest, &dFiles, &dDirs)
	for _, f := range dFiles {
		rel, err := filepath.Rel(dest, f)
		if err != nil {
			continue
		}
		if _, ok := wanted[posix(rel)]; !ok {
			if err := os.Remove(f); err == nil {
				st.Deleted++
				e.warn("删除残留[%s]: %s", dest, posix(rel))
			}
		}
	}
	sort.Slice(dDirs, func(i, j int) bool { return len(dDirs[i]) > len(dDirs[j]) })
	for _, d := range dDirs {
		rel, err := filepath.Rel(dest, d)
		if err != nil {
			continue
		}
		if wantedDirs[posix(rel)] {
			continue // 清单内含空目录 => 保留结构
		}
		if entries, err := os.ReadDir(d); err == nil && len(entries) == 0 {
			if os.Remove(d) == nil {
				st.DirsRemoved++
			}
		}
	}

	return st, nil
}

// copyFile 复制并保留 mtime(避免下次误判为变更)。目标以只读方式重写。
func copyFile(src, dst string, si os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chtimes(tmp, si.ModTime(), si.ModTime()); err != nil {
		// 时间戳设置失败不致命
		_ = err
	}
	return os.Rename(tmp, dst)
}
