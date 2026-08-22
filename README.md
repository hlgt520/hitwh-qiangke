# qiangke —— HITWH 抢课脚本（Go）

针对 `jwts.hitwh.edu.cn`（教务系统）的高速抢课工具，校园网直连。

## 快捷使用方法

### 1.打开网址'https://github.com/hlgt520/hitwh-qiangke'从realse中下载最新压缩包

### 2.解压缩之后，双击qiangke.exe，等待自动跳转至http://127.0.0.1:8080，扫码登陆即可使用（首次登录可能会触发二次验证，点击发送短信验证码，输入验证码即可使用）

### 3.目前仅限校园网环境，且必须是哈工大APP扫码登录才可使用。

## 免责声明

仅供学习交流，抢课后果自负；请遵守学校选课规则。




## 环境配置（从源码构建）

### 1. 安装 Go

- 官网下载：<https://go.dev/dl/>（国内可访问 <https://golang.google.cn/dl/>）
- Windows 选 `.msi` 安装包，一路下一步即可（默认装到 `C:\Program Files\Go`）
- 装完开新终端验证：

```bash
go version
```

### 2. 从 GitHub 拉取（可选）

```bash
git clone https://github.com/hlgt520/hitwh-qiangke.git
cd hitwh-qiangke
```

或者直接在https://github.com/hlgt520/hitwh-qiangke中打包源码

```bash
cd hitwh-qiangke
```

### 3. 配置国内模块代理

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

### 4. 拉取依赖并构建

```bash
cd qiangke
go mod tidy                 # 自动下载依赖
go build -o qiangke.exe .   # 编译成单文件 exe
```

编译完成后得到 `qiangke.exe`，双击即可运行。

> 依赖说明：仅 `golang.org/x/net`（用于 GBK/GB2312 网页解码），其余全是 Go 标准库；`go.sum` 已锁定依赖版本。


## 设计要点（防冻结）

- **登录一次、cookie 落盘复用**：`cookie.json`，再次运行直接复用，绝不重复登录。
- **退出时释放 CAS 会话**：CLI 正常结束 / Ctrl+C 尽力登出；Web 模式点叉号关窗或 Ctrl+C 也会在退出前自动登出（关标签页也会通知脚本登出，抢课进行中除外），避免僵尸会话累积到「并行会话数 ≥10」。
- **单 IP 单会话**：校园网固定出口。
- **等待期低频保活**：默认 30s 一次，不空转刷请求。
- **限速 + 风控熔断**：串行「取 token→提交」，最小间隔 + 随机抖动；出现「频繁/冻结」立即停手。

## 用法

```bash
cd qiangke
go build -o qiangke.exe .    # 已附带编译好的 qiangke.exe
```

### Web 界面（默认，双击 .exe 即可）

双击 `qiangke.exe` 会自动启动本地 Web 界面并打开浏览器（`http://127.0.0.1:8080`），全程在浏览器里操作：

1. **登录**：点「生成二维码登录」→ 扫码确认；
2. **选课**：选学期/类别 → 搜索 → 勾选课程 → 添加（可跨类别多选、可删除）；
3. **开抢**：填开闸时间（留空=立即）→ 点「开始抢课」；
4. **进度**：实时查看抢课日志与结果。

> 若浏览器没自动打开，手动访问 <http://127.0.0.1:8080>。

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

> 说明：双击默认进 Web 界面；带任何命令行参数则进命令行模式。抢课核心逻辑两种模式完全一致，Web 界面只是本地前端，不影响抢课速度。

参数：
| 参数 | 默认 | 说明 |
|---|---|---|
| `-c` | cookie.json | cookie 持久化文件 |
| `-xnxq` | 交互 | 学年学期，如 `2026-20271`（秋季）|
| `-xklb` | 交互 | 类别：yy/ty/szhx/cxyx/cxsy/cxcy/xsyt/tsk/xsxk/sxw |
| `-rwh` | 交互 | 课程任务号（提交用，完整任务号）|
| `-targets` | 空 | 多课程 `xklb:rwh,xklb:rwh` |
| `-t` | 空 | 开闸时间 `2006-01-02 15:04:05`；留空=立即 |
| `-relogin` | false | 强制重新扫码 |
| `-keepalive` | 30 | 等待期保活间隔(秒) |

## 抢课时间要点

1. **提前 30–60 分钟**：`-login-only` 登录一次，存下 cookie。
2. **选课前抓课表锁 rwh**：课程表公布后，用交互模式选好目标课，记下 `rwh`（或直接 -rwh 指定）。
3. **开闸**：`-t` 指定开闸时间，程序会保活等待、到点串行「取 token → 提交」直到成功/容量满/风控。

## 免责声明

仅供学习交流，抢课后果自负；请遵守学校选课规则。
