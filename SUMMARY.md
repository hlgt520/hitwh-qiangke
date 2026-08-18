# HITWH 抢课脚本 —— 完整交接总结

> 生成时间：2026-08-16 · 用于在其他会话继续开发
> 仓库：https://github.com/hlgt520/hitwh-qiangke （公开）
> 本地目录：`X:\DeepSeek Harness\抢课脚本\qiangke`

---

## 1. 项目是什么

针对 **哈尔滨工业大学(威海) 教务系统**（青果教务，`jwts.hitwh.edu.cn`）的抢课工具。
扫码登录（HIT APP/企业微信）→ 查课表 → 多课程跨类别 → 定时/立即抢课 → 防冻结。

技术栈：**Go**（仅依赖 `golang.org/x/net`），本地 **Web 界面**（`web.go` + `web/index.html`，默认 http://127.0.0.1:8080），GitHub Actions 自动构建 Release。

---

## 2. 架构与关键接口

| 项 | 说明 |
|---|---|
| CAS 统一认证 | `https://ids.hit.edu.cn/authserver`（直连，**必须直连才能拿 JSESSIONID**） |
| 教务系统 | `http://jwts.hitwh.edu.cn`（**校园网直连，无需 WebVPN**） |
| 查课表 | `POST /xsxk/queryXsxkList`（表单：pageXnxq/pageXklb/pageNo/pageSize/pageCount/token/...） |
| 提交选课 | `POST /xsxk/saveXsxk`（表单：pageXnxq/pageXklb/rwh/token + 空字段） |
| 登录回调 | `service = http://jwts.hitwh.edu.cn/loginCAS`（CAS 登录后跳这里建教务会话） |

**扫码登录流程**：getToken(uuid) → getCode(二维码PNG) → 轮询 getStatus(1=确认) → GET 登录页取 execution → POST 登录表单 → 跳转 loginCAS 建会话 → cookie 落盘 `cookie.json`。

---

## 3. 关键实测结论（最重要，别改错）

1. **token 单次有效**：每次查询都生成新 token，旧 token 失效 → **必须串行「取新 token → 提交」，不能并发**。
2. **查课表要两步**：第一次（无 token）→ 拿到 token + pageCount；第二次（带 token + pageCount）→ 才返回课程列表。脚本已自动处理（`fetchCourses`）。
3. **`fetchToken` 一次查询即可**：单次无 token 查询拿到的 token **能直接用于提交**（已实测，见 `courses.go` 的 `fetchToken`）。
4. **rwh 是完整任务号**：形如 `2026-2027-1-22WHAE43002-001`（学期+课程号+班号），从课程行最后一列 `<input id="xkyq_<rwh>">` 提取（`courses.go` 的 `parseCourses`）。
5. **提交已被服务器接受**：实测返回「学生未注册/选课未开放」（参数正确），不是「非法操作」。
6. **「未注册」「不在选课时间」= 选课未开放** → 重试，不是停止（`isDecisive` 不含这两项）。
7. **「非法操作」= token 过期** → 重试（v0.1.2 起从"停止"改为"重试"，每轮重新取 token）。
8. **冻结规则**：`累计并行会话数≥10 或 IP数≥10 → 冻结3分钟`。由**反复登录**触发（每次登录=新会话），**不是请求频率**。防冻结靠：登录一次+复用 cookie、退出时登出、单会话。

---

## 4. 已实现功能（全部实测可用）

- ✅ 扫码登录（CAS 直连二维码）
- ✅ 课表查询（翻页 + 关键词过滤）
- ✅ 多课程 + 跨类别（交互式循环加课；命令行 `-targets "xklb:rwh,xklb:rwh"`）
- ✅ 定时抢课（`-t` 支持绝对时间 `2026-8-15 22:09:00`、相对时间 `+30s`/`+5m`）
- ✅ 防冻结（单会话+复用+退出登出+风控熔断：检测"频繁/冻结"立即停）
- ✅ 掉线自动重试
- ✅ 结束不闪退（按回车退出 + panic 捕获）
- ✅ 本地 Web 界面（扫码登录/课表搜索多选/定时/实时进度/停止按钮）
- ✅ 抢课提速（v0.1.2：去掉限速器+退避；「非法操作」改重试）
- ✅ GitHub Actions 自动构建并发布 Release（推送 `v*` tag 触发）

---

## 5. 防冻结设计（核心）

1. 登录一次、cookie 落盘复用（`cookie.json`），绝不重复登录；
2. 退出时（正常结束/Ctrl+C）主动登出 CAS 释放会话；
3. 校园网固定出口（单 IP）；
4. 等待期低频保活（默认 30s）；
5. 抢课循环检测到「频繁/冻结/并行会话/IP数」→ **立即熔断停止**。

---

## 6. 当前代码结构（`qiangke/`）

| 文件 | 作用 |
|---|---|
| `main.go` | CLI 入口：登录/选目标/定时/抢课/Web 启动 |
| `web.go` | 本地 Web 服务（`/api/*`） |
| `web/index.html` | Web 前端 |
| `login.go` | 扫码登录 + 密码登录(休眠) + 二次验证 MFA(休眠) |
| `courses.go` | 查课表/解析课程/取 token |
| `submit.go` | 提交选课/结果判定/抢课循环 |
| `net.go` | HTTP 客户端、cookie 持久化、端点常量 |
| `encrypt.go` | 密码 AES 加密（密码登录用，休眠） |
| `account.go` | 账号配置结构（密码登录用，休眠） |
| `console_windows.go` / `console_other.go` | 控制台辅助 |
| `build-release.ps1` | 一键打包（386+amd64 zip） |
| `.github/workflows/release.yml` | CI 自动构建发布 |

---

## 7. 当前状态

- **Git**：`main` 分支干净，已推送；tags：`v0.1.0`、`v0.1.1`、**`v0.1.2`**（HEAD=fe948b1）
- **git 配置**：user.name=`hlgt520`；凭据存 `~/.git-credentials`（store helper）；代理=`http://127.0.0.1:7890`（Clash，**当前在线**，GitHub 需走它）
- **登录状态**：`cookie.json` 会话可能已过期（之前抢课报过 404）→ 下次测试需重新扫码登录

---

## 8. 待办 / 可继续的方向

1. **token 链式复用**（潜在再提速 ~2x）：从"提交响应"里直接提取下一个 token，省掉每轮的"取 token 查询"。**需实测确认：提交响应的 token 能否直接用于下一次提交**。
2. **Web 前端登录状态实时刷新**：前端 `loadState()` 只在页面加载/登录/退出时调用，没有轮询 `/api/state`，后端会话失效后徽章最多延迟 60s（保活检测）才变，且要刷新页面才更新。可加 10s 轮询。
3. **密码登录**（休眠代码）：MFA(短信) 已打通到 `reAuth_success`，但**最后一步登录完成有 bug**（GET /login 会再次跳回二次验证页，教务会话未建立）。用户已决定**搁置**，用扫码方案。若续做，排查点：reAuthSubmit.do 成功后为何 /login 又触发 MFA。
4. **校外 WebVPN 路径**（未实现，低优先级）：校园网外需走 WebVPN，链路已摸清但未接入。

---

## 9. 使用方法

```bash
# Web 界面（默认，双击 exe 或命令行运行）
.\qiangke.exe            # → 打开 http://127.0.0.1:8080

# 命令行模式（带参数时自动进入）
.\qiangke.exe -login-only                          # 仅登录存 cookie
.\qiangke.exe -xnxq 2026-20271 -targets "cxyx:xxx,cxsy:yyy" -t "+30s"   # 定时多课程抢

# 构建
go env -w GOPROXY=https://goproxy.cn,direct
go build -o qiangke.exe .
```

**抢课节奏**：选课前 30-60 分钟 `-login-only` 登录 → 开抢用 `-t` 定时（或提前开抢循环，v0.1.2 已提速）。

---

## 10. ⚠️ 安全提醒

- `~/.git-credentials` 里存了 GitHub token（曾明文出现在聊天记录）——**建议用户尽快在 GitHub 撤销并重新生成**。
- 学号 `2023212052` 曾在聊天中暴露；`account.json`（含密码）已删除，`.gitignore` 已排除。
- 敏感文件不入库：`cookie.json`、`account.json`、`*.exe`、`*.png`。

---

## 11. 网络注意事项

- 校园网内直连 `jwts.hitwh.edu.cn` 可用；GitHub 需走 Clash 代理（127.0.0.1:7890，当前在线）。
- 若 Clash 关了、push 卡住，重开 Clash 即可；凭据已存，无需再输 token。
