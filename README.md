# backup-c

把清单中的文件/目录**以源为准**完整镜像备份到目标目录（默认 `dist/`）。编译为单个 `backup.exe`，
由 **Windows 计划任务**（或手动）启动，启动后**每隔 `interval` 分钟**自动备份一次。

- 源文件全程**只读**，绝不修改。
- 支持备份**单个文件**、**整个目录**（递归）与 **glob**。
- 清单支持 `include` / `exclude` 两个字段，**exclude 优先级更高**。
- 支持**多个目标目录**（`-d "D:\a,E:\b"`，或用 `config.targetDir` 列表）。
- 备份间隔用 `-i` 或 `config.interval` 指定，默认 `<exe目录>/dist` 与 10 分钟。
- 单文件分发，无运行时依赖（不需要 Node / Python）。
- 单实例保护：同一时刻只允许一个 `run` 循环在跑。目标目录即使在源内也不会自我递归。

> 快速开始：把 `config.example.yaml` 复制为 `config.yaml`，填上自己的 `sourceRoot`
> （`config.yaml` 已被 `.gitignore` 忽略，你的真实路径不会进仓库）。

## 工作方式

```
Windows 计划任务 / 手动双击
        │  启动
        ▼
   backup.exe run  ──►  立即备份一次（写到所有目标目录）
        │
        ├─►  每隔 interval 分钟再备份一次（循环，直到退出）
        │
        └─►  持续监听 config.yaml：一旦变化就重载配置
             （interval / targetDir / include / exclude 全部生效）
             并立即备份一次
```

取值优先级：

| 项目 | 优先级 |
| --- | --- |
| 备份间隔 | 命令行 `-i` → `config.yaml` 的 `interval` → 默认 **10 分钟** |
| 目标目录 | 命令行 `-d` → `config.yaml` 的 `targetDir` → 默认 **`<exe目录>\dist`**（可多个） |

> 改完 `config.yaml` 保存即生效——无需重启进程，循环会在约 2 秒内检测到并重载。

## 目录结构

```
backup-c/
├─ src/                 # Go 源码
│  ├─ main.go           # 入口：子命令 + 循环逻辑
│  ├─ engine.go         # 备份引擎（镜像语义）
│  ├─ logger.go         # 文件日志，按天切分
│  ├─ lock_windows.go   # 单实例锁
│  ├─ task_windows.go   # 计划任务管理（schtasks）
│  ├─ engine_test.go    # 单元测试
│  ├─ go.mod / go.sum
├─ .github/workflows/   # CI：测试 + 打包（Windows），打 tag 时发 Release
├─ config.example.yaml  # 清单模板：复制为 config.yaml 后按需修改
├─ config.yaml          # 你的真实清单（本地，已被 .gitignore 忽略）
├─ build.bat            # 一键构建 → 输出到 release/
├─ release/             # 构建产物（已被忽略）：backup.exe + 起步 config.yaml
│  ├─ backup.exe        # 发行版 exe
│  ├─ config.yaml       # 由 config.example.yaml 复制而来
│  └─ logs/             # 运行日志 backup-YYYY-MM-DD.log（首次运行后生成）
└─ dist/                # 默认备份目标（include 减 exclude 的完整镜像）
```

> 程序从**自身 exe 所在目录**定位 `config.yaml` / `logs/`，不依赖当前工作目录。
> 因此 `release/` 是**自包含**的：整个文件夹拷到任何地方都能直接跑。
> 备份目标默认写到同目录的 `dist/`（可用 `-d` 或 `config.targetDir` 改，支持多个）。

## 构建

一键构建（编译优化 + UPX 压缩，产物输出到 `release/`）：

```bat
build.bat
```

产物：`release/backup.exe`，以及从 `config.example.yaml` 复制出的 `release/config.yaml`
（若已存在则不覆盖，避免冲掉你自己的路径）。整个 `release/` 即可直接分发。

或手动：

```bat
cd src
set GOPROXY=https://goproxy.cn,direct
go build -ldflags "-s -w" -trimpath -o ..\release\backup.exe .
```

> - `-s -w` 去掉符号表/DWARF，`-trimpath` 去掉源码路径：**4.6MB → 3.0MB**。
> - 再经 **UPX** 压缩可到 **约 1.2MB**（38%）。需先 `scoop install upx`（或用任意方式装 upx 到 PATH）。
> - 无控制台窗口版本：`go build -ldflags "-s -w -H=windowsgui" -trimpath -o ..\release\backup.exe .`
>   （无窗口时 stdout 不可见，但日志仍写入 `logs/`。）

### 关于 UPX 与杀毒软件

UPX 压缩会显著减小体积，但**压缩过的 exe 较易被杀毒软件启发式误报**（这是壳的常见副作用）。
若被拦截，改用未压缩版本（跳过 `build.bat` 的 UPX 步骤，或直接 `go build`）即可 —— 功能完全相同。

### CI（GitHub Actions）

`.github/workflows/build.yml` 在 **windows-latest** 上自动跑（仅 Windows 平台，
因为 `lock_windows.go` / `task_windows.go` 带 `//go:build windows`）：

- 每次 push / PR：`gofmt` 检查 → `go vet` → `go test` → 构建（压缩 + 未压缩两个 exe）→ 上传 Artifacts；
- 推送 **`v*` 标签**时额外创建 GitHub Release，附上 `backup.exe`、`backup-uncompressed.exe` 与 `config.yaml`。

版本号在构建时注入（`-X main.version=...`）：打 tag 时用 tag 名，否则用 `0.0.0-dev+<短sha>`。
发布一个版本：

```bat
git tag v1.0.0
git push origin v1.0.0
```

## 使用

```bat
backup.exe run                          :: 启动后每 interval 分钟备份一次（默认）
backup.exe run -i 20                    :: 本次以 20 分钟间隔跑
backup.exe run -d D:\backup             :: 本次备份到指定目录
backup.exe run -d "D:\a,E:\b"           :: 本次备份到多个目录（逗号分隔）
backup.exe run -i 20 -d "D:\a,E:\b"     :: 间隔与目标都可临时指定
backup.exe once                         :: 立即备份一次后退出
backup.exe once -d "D:\a,E:\b"          :: 立即备份一次到多个目录
backup.exe install                      :: 注册 Windows 计划任务（登录时自动启动，需管理员）
backup.exe install -i 20 -d "D:\a,E:\b" :: 注册任务并固定间隔与目标
backup.exe uninstall                    :: 删除计划任务（需管理员）
backup.exe status                       :: 查看计划任务状态
backup.exe version                      :: 显示版本
```

选项也支持长写法：`--interval=20`、`--target="D:\a,E:\b"`。

### 安装为开机自启（推荐）

以**管理员**运行一次：

```bat
backup.exe install
```

它会注册计划任务 `backup-c`，触发条件为**登录时**启动 exe；exe 启动后按 `interval`
分钟循环。如需指定间隔与目标目录：

```bat
backup.exe install -i 20 -d "D:\a,E:\b"
```

（`-i` / `-d` 会被写进任务命令行，登录拉起后仍生效。）

### 热重载 config.yaml

`run` 循环会持续监听 `config.yaml`（每约 2 秒检测一次）。文件一改动，就：

1. 重新加载配置（`interval` / `targetDir` / `include` / `exclude` 全部按新值生效）；
2. 若 `interval` 变了，按新间隔重置定时器；
3. **立即备份一次**，让新配置马上落地。

所以调完配置**保存即生效，不用重启进程**。命令行传的 `-i` / `-d` 仍然优先，不会被 config 覆盖。

日志示例：

```
进入循环: 每 10 分钟同步一次 (间隔来源: config.interval, ...)
[重载] 检测到 config.yaml 变化,重新加载配置并立即备份...
[重载] 同步间隔已改为每 1 分钟 (间隔来源: config.interval)
[重载] 备份完成: 新增 0 更新 0 未变 1 删除 0 空目录复原 0 清理空目录 0 (1ms)
```

> 计划任务由 `schtasks /sc onlogon` 创建，**需要管理员权限**。
> 若你只想手动或用自己的方式启动，直接双击/命令行跑 `backup.exe run` 即可，无需安装。

## 清单（config.yaml）

仓库里提供模板 `config.example.yaml`，复制为 `config.yaml` 后修改（后者已被 `.gitignore` 忽略）：

```bat
copy config.example.yaml config.yaml
```

```yaml
sourceRoot: "C:\\Users\\YourName"   # 源根目录（绝对路径，建议显式指定）

targetDir: "D:\\backup"             # 备份目标目录（可选；不设则用 <exe>\dist，-d 可覆盖）
# 多个目标二选一：
# targetDir: "D:\\a,E:\\b"          # 逗号分隔
# targetDir:                        # 或 YAML 列表
#   - "D:\\a"
#   - "E:\\b"

interval: 10                        # 备份间隔（分钟）。-i 参数可临时覆盖；不设则默认 10

include:                            # 要备份的路径/模式（相对 sourceRoot）
  - Documents                       # 整个目录（递归）
  - Projects/my-app
  - .gitconfig                      # 单个文件
  - ".config/**/*.json"             # glob 通配

exclude:                            # 排除规则，优先级高于 include
  - .git                            # 不含 / 的规则按「任意层级的名字」匹配；命中目录 => 整棵子树跳过
  - .vscode
  - .trae
  - node_modules
  - "*.log"
  # - Projects/my-app/dist          # 含 / 的规则按整路径 glob，也匹配祖先目录
```

### 为什么要有 `sourceRoot`

计划任务/服务可能以与你不同的账户运行，用户主目录不一定是你的主目录。
因此建议**显式配置 `sourceRoot`** 为绝对路径；只有未配置时才回退到用户主目录（会打印警告）。
程序也从**自身 exe 所在目录**定位 `config.yaml`、`logs/`，不依赖当前工作目录；
备份目标默认写到同目录的 `dist/`（可用 `-d` 或 `config.targetDir` 改，支持多个）。

### 匹配写法

| 写法 | 含义 |
| --- | --- |
| `.gitconfig` | 单个文件 |
| `.ssh` | 目录，递归带出其中所有文件与子目录 |
| `.ssh/**` | glob 通配；含 `/` 的规则按整路径匹配，并匹配各级祖先目录 |
| `*.log` / `node_modules` | 不含 `/` 的规则按「任意层级的名字」匹配 |

### 备份语义（镜像同步）

目标目录是 **include 减 exclude** 的完整镜像：

- 源的文件**结构与数据**都被忠实复制，**空目录也会保留**；
- 源里**删除/改名**的文件、或被新规则排除的文件，下次备份会从目标目录清除；
- 目标是源的精确副本，**以源为准**。

### 安全约束

- 清单路径必须是**相对 `sourceRoot`** 的路径，禁止绝对路径与 `..`；
- **源位于备份目标内部**（即目标写在源里，如 `-d ~/backup`）时会被拒绝；
- **备份目标位于源内部**时，遍历源会自动跳过目标目录自身，不会自我递归；
- 采用「**先复制后删除**」，中途失败不会丢失已有备份。

## 日志

写到 exe 旁的 `logs/backup-YYYY-MM-DD.log`，按天切分：

```
2026-10-16 10:50:36 [启动] 备份完成: 新增 1 更新 0 未变 0 删除 0 空目录复原 1 清理空目录 0 (2ms)
2026-10-16 10:50:36 进入循环: 每 1 分钟同步一次 (间隔来源: -i 参数, ...)
2026-10-16 10:51:36 [定时] 备份完成: 新增 0 更新 0 未变 1 删除 0 空目录复原 0 清理空目录 0 (1ms)
```

## 测试

```bat
cd src
go test -v .
```

覆盖：include（文件/目录/glob）、exclude 优先（文件 + 目录子树 + 任意层级名字）、
镜像增删、空目录结构保留、源只读、路径安全校验、interval 优先级、目标目录优先级与多目标、
`-i` / `-d` 参数解析、逗号分隔解析、config 变化指纹检测。
