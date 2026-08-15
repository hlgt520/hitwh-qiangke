# qiangke —— HITWH 抢课脚本（Go）

针对 `jwts.hitwh.edu.cn`（青果教务系统）的高速抢课工具，校园网直连。

## 已验证（2026-08 实测，用真实课表全链路打通）

- ✅ 扫码登录链路：CAS（ids.hit.edu.cn）二维码 → 教务系统建立会话
- ✅ 校园网直连 `jwts.hitwh.edu.cn`（无需 WebVPN）
- ✅ 课表查询：`queryXsxkList` 需**先取 token+pageCount，再带两者查询**才返回课程（脚本已自动处理）
- ✅ 课程解析：正确提取 `rwh`（完整任务号，形如 `2026-2027-1-22WHAE43002-001`）与课程名
- ✅ 提交 `saveXsxk`：token + rwh 格式被服务器接受（选课未开放时返回「学生未注册」，开放后即「选课成功」）
- ✅ cookie 持久化/复用（会话可跨进程复用）
- ⚠️ token **单次有效**（每次请求都变，不能复用 → 串行「取 token→提交」）

> 说明：页面常驻「学生未进行注册，不可选课！」提示，含义是**选课尚未开放**；课表仍可正常查看，等选课开放、完成注册后提交即生效。

## 环境配置（从源码构建）

本工具是 Go 程序，构建/运行**只需安装 Go**，无其他运行时依赖。仓库里已附带编译好的 `qiangke.exe`，若只想直接用可跳过本节。

### 1. 安装 Go

- 官网下载：<https://go.dev/dl/>（国内可访问 <https://golang.google.cn/dl/>）
- Windows 选 `.msi` 安装包，一路下一步即可（默认装到 `C:\Program Files\Go`）
- 装完开新终端验证：

```bash
go version
```

### 2. 配置国内模块代理（重要）

默认的 `proxy.golang.org` 在国内经常连不上，**必须先换成国内镜像**：

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

### 3. 拉取依赖并构建

```bash
cd qiangke
go mod tidy                 # 自动下载依赖
go build -o qiangke.exe .   # 编译成单文件 exe
```

编译完成后得到 `qiangke.exe`，双击即可运行。

> 依赖说明：仅 `golang.org/x/net`（用于 GBK/GB2312 网页解码），其余全是 Go 标准库；`go.sum` 已锁定依赖版本。

### 4. 从 GitHub 拉取（可选）

```bash
git clone https://github.com/hlgt520/hitwh-qiangke.git
cd hitwh-qiangke
# 然后按上面 2、3 步配置代理并构建
```

## 设计要点（防冻结）

- **登录一次、cookie 落盘复用**：`cookie.json`，再次运行直接复用，绝不重复登录。
- **退出时释放 CAS 会话**：正常结束 / Ctrl+C 尽力登出，避免僵尸会话累积到「并行会话数 ≥10」。
- **单 IP 单会话**：校园网固定出口。
- **等待期低频保活**：默认 30s 一次，不空转刷请求。
- **限速 + 风控熔断**：串行「取 token→提交」，最小间隔 + 随机抖动；出现「频繁/冻结」立即停手。

## 用法

```bash
cd qiangke
go build -o qiangke.exe .    # 已附带编译好的 qiangke.exe
```

### 交互式（推荐，双击 .exe 即可）
```bash
.\qiangke.exe
# 依次：学年学期 → 循环「选类别 → 关键词过滤 → 选课程 → 是否加下一门」→ 是否定时 → 按顺序抢
# 全程纯菜单操作，支持多课程、跨类别（如同时抢创新研修 + 创新实验）
```

### 命令行（多课程/定时）
```bash
# 单门课立即提交
.\qiangke.exe -xnxq 2026-20271 -xklb szhx -rwh <课程号>

# 多门课 + 跨类别（按顺序抢，xklb:rwh 逗号分隔）
.\qiangke.exe -xnxq 2026-20271 -targets "cxyx:xxx,cxsy:yyy" -t "2026-08-25 12:30:00"

# 强制重新扫码登录 / 仅登录存 cookie
.\qiangke.exe -relogin
.\qiangke.exe -login-only
```

参数：
| 参数 | 默认 | 说明 |
|---|---|---|
| `-c` | cookie.json | cookie 持久化文件 |
| `-xnxq` | 交互 | 学年学期，如 `2026-20271`（秋季）|
| `-xklb` | 交互 | 类别：yy/ty/szhx/cxyx/cxsy/cxcy/xsyt/tsk/xsxk/sxw |
| `-rwh` | 交互 | 课程任务号（提交用，完整任务号）|
| `-targets` | 空 | 多课程 `xklb:rwh,xklb:rwh` |
| `-t` | 空 | 开闸时间 `2006-01-02 15:04:05`；留空=立即 |
| `-interval` | 150 | 提交最小间隔(ms) |
| `-retry` | 300 | 瞬时失败最大重试轮数 |
| `-relogin` | false | 强制重新扫码 |
| `-keepalive` | 30 | 等待期保活间隔(秒) |

## 抢课时间要点

1. **提前 30–60 分钟**：`-login-only` 登录一次，存下 cookie。
2. **选课前抓课表锁 rwh**：课程表公布后，用交互模式选好目标课，记下 `rwh`（或直接 -rwh 指定）。
3. **开闸**：`-t` 指定开闸时间，程序会保活等待、到点串行「取 token → 提交」直到成功/容量满/风控。

## 免责声明

仅供学习交流，抢课后果自负；请遵守学校选课规则。
