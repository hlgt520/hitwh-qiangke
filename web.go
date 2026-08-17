package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html
var webHTML []byte

var (
	webClient     *http.Client
	webLoggedIn   bool
	webLoggedInMu sync.Mutex

	webGrabState = &grabState{}

	qrCacheMu sync.Mutex
	qrCache   = map[string][]byte{}
	qrExec    = map[string]string{}
)

type grabState struct {
	mu      sync.Mutex
	running bool
	done    bool
	result  string
	current string
	results []grabResult
	log     []string
}

type grabResult struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Ok     bool   `json:"ok"`
}

func (s *grabState) addLog(format string, a ...interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := time.Now().Format("15:04:05.000")
	s.log = append(s.log, fmt.Sprintf("[%s] %s", ts, fmt.Sprintf(format, a...)))
	if len(s.log) > 800 {
		s.log = s.log[len(s.log)-800:]
	}
}

func (s *grabState) snapshot() (running, done bool, result, current string, results []grabResult, log []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resultsCopy := append([]grabResult(nil), s.results...)
	return s.running, s.done, s.result, s.current, resultsCopy, append([]string(nil), s.log...)
}

func setLoggedIn(v bool) {
	webLoggedInMu.Lock()
	webLoggedIn = v
	webLoggedInMu.Unlock()
}

func getLoggedIn() bool {
	webLoggedInMu.Lock()
	defer webLoggedInMu.Unlock()
	return webLoggedIn
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": msg})
}

// —— 页面 ——
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(webHTML)
}

// —— 会话状态 ——
func handleState(w http.ResponseWriter, r *http.Request) {
	running, done, result, current, results, log := webGrabState.snapshot()
	kt, kok, khas := keepaliveSnapshot()
	writeJSON(w, map[string]interface{}{
		"loggedIn": getLoggedIn(),
		"running":  running,
		"done":     done,
		"result":   result,
		"current":  current,
		"results":  results,
		"log":      log,
		"keepalive": map[string]interface{}{
			"time": kt,
			"ok":   kok,
			"has":  khas,
		},
	})
}

// —— 登录 ——
func handleLoginQR(w http.ResponseWriter, r *http.Request) {
	uuid, err := getQRUUID(webClient)
	if err != nil {
		writeErr(w, 500, "获取二维码失败: "+err.Error())
		return
	}
	execution, err := getLoginExecution(webClient)
	if err != nil {
		writeErr(w, 500, "获取登录页失败: "+err.Error())
		return
	}
	png, err := getQRCodePNG(webClient, uuid)
	if err != nil {
		writeErr(w, 500, "生成二维码失败: "+err.Error())
		return
	}
	qrCacheMu.Lock()
	qrCache[uuid] = png
	qrExec[uuid] = execution
	qrCacheMu.Unlock()
	writeJSON(w, map[string]string{"uuid": uuid})
}

func handleLoginQRImage(w http.ResponseWriter, r *http.Request) {
	uuid := r.URL.Query().Get("uuid")
	qrCacheMu.Lock()
	png := qrCache[uuid]
	qrCacheMu.Unlock()
	if png == nil {
		writeErr(w, 404, "二维码不存在或已过期")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}

func handleLoginStatus(w http.ResponseWriter, r *http.Request) {
	uuid := r.URL.Query().Get("uuid")
	st, err := getQRStatus(webClient, uuid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	status := "idle"
	switch strings.TrimSpace(st) {
	case "1":
		status = "confirmed"
	case "2":
		status = "scanned"
	case "3":
		status = "expired"
	}
	writeJSON(w, map[string]string{"status": status})
}

func handleLoginConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Uuid string `json:"uuid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Uuid == "" {
		writeErr(w, 400, "uuid 为空")
		return
	}
	qrCacheMu.Lock()
	execution := qrExec[req.Uuid]
	qrCacheMu.Unlock()
	if execution == "" {
		writeErr(w, 400, "二维码状态异常，请重新生成")
		return
	}
	if err := doCASLogin(webClient, req.Uuid, execution); err != nil {
		writeErr(w, 500, "登录失败: "+err.Error())
		return
	}
	if err := saveCookies(webClient, "cookie.json"); err != nil {
		writeErr(w, 500, "保存会话失败: "+err.Error())
		return
	}
	setLoggedIn(true)
	resetKeepalive() // 登录成功后清除之前的"会话失效"告警状态
	writeJSON(w, map[string]bool{"ok": true})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	doLogout(webClient)
	setLoggedIn(false)
	writeJSON(w, map[string]bool{"ok": true})
}

// —— 元数据 ——
func handleCourseTypes(w http.ResponseWriter, r *http.Request) {
	type ct struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	out := make([]ct, 0, len(courseTypeList))
	for _, t := range courseTypeList {
		out = append(out, ct{Key: t.key, Label: t.label})
	}
	writeJSON(w, out)
}

func handleSemesters(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	year := now.Year()
	type sem struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	var out []sem
	for y := year + 1; y >= year-3; y-- {
		out = append(out,
			sem{Value: fmt.Sprintf("%d-%d%d", y, y+1, 1), Label: fmt.Sprintf("%d 秋季", y)},
			sem{Value: fmt.Sprintf("%d-%d%d", y-1, y, 2), Label: fmt.Sprintf("%d 春季", y)},
			sem{Value: fmt.Sprintf("%d-%d%d", y-1, y, 3), Label: fmt.Sprintf("%d 夏季", y)},
		)
	}
	writeJSON(w, out)
}

// —— 课程查询 ——
func handleCourses(w http.ResponseWriter, r *http.Request) {
	xnxq := r.URL.Query().Get("xnxq")
	xklb := r.URL.Query().Get("xklb")
	keyword := r.URL.Query().Get("keyword")
	if xnxq == "" || xklb == "" {
		writeErr(w, 400, "请先选择学年学期和课程类别")
		return
	}
	courses, _, err := fetchCourses(webClient, xnxq, xklb)
	if err != nil {
		writeErr(w, 500, "拉取课程失败（会话可能失效）: "+err.Error())
		return
	}
	if keyword != "" {
		var filtered []Course
		for _, c := range courses {
			if strings.Contains(c.Name, keyword) || strings.Contains(c.KcCode, keyword) || strings.Contains(c.Code, keyword) {
				filtered = append(filtered, c)
			}
		}
		courses = filtered
	}
	writeJSON(w, map[string]interface{}{"count": len(courses), "courses": courses})
}

// —— 抢课 ——
func handleGrab(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Xnxq    string   `json:"xnxq"`
		Targets []Target `json:"targets"`
		Trigger string   `json:"trigger"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if req.Xnxq == "" || len(req.Targets) == 0 {
		writeErr(w, 400, "请先选择课程（目标和学年学期不能为空）")
		return
	}

	webGrabState.mu.Lock()
	if webGrabState.running {
		webGrabState.mu.Unlock()
		writeErr(w, 409, "抢课已在进行中")
		return
	}
	webGrabState.running = true
	webGrabState.done = false
	webGrabState.result = ""
	webGrabState.current = ""
	webGrabState.results = nil
	webGrabState.log = nil
	webGrabState.mu.Unlock()

	go webGrabRun(req.Xnxq, req.Targets, req.Trigger)
	writeJSON(w, map[string]bool{"ok": true})
}

func handleGrabStatus(w http.ResponseWriter, r *http.Request) {
	running, done, result, current, results, log := webGrabState.snapshot()
	writeJSON(w, map[string]interface{}{
		"running": running,
		"done":    done,
		"result":  result,
		"current": current,
		"results": results,
		"log":     log,
	})
}

func handleGrabStop(w http.ResponseWriter, r *http.Request) {
	grabStop.Store(true)
	writeJSON(w, map[string]bool{"ok": true})
}

func webGrabRun(xnxq string, targets []Target, trigger string) {
	logf := webGrabState.addLog
	grabStop.Store(false)
	defer func() {
		if rec := recover(); rec != nil {
			logf("程序异常: %v", rec)
		}
		webGrabState.mu.Lock()
		webGrabState.running = false
		webGrabState.done = true
		webGrabState.current = ""
		webGrabState.mu.Unlock()
	}()

	// 定时等待
	if trigger != "" {
		target, err := parseTriggerTime(trigger)
		if err != nil {
			logf("时间格式错误: %v", err)
			webGrabState.result = "时间格式错误"
			return
		}
		offset := measureClockOffset(webClient)
		logf("目标开闸时间(服务器): %s", target.Format("2006-01-02 15:04:05"))
		webGrabState.current = "等待开闸..."
		for {
			if grabStop.Load() {
				logf("已手动停止（等待阶段）")
				webGrabState.result = "已停止"
				return
			}
			serverNow := time.Now().Add(offset)
			if !serverNow.Before(target) {
				break
			}
			remain := target.Sub(serverNow)
			if remain > 30*time.Second {
				if _, err := fetchToken(webClient, xnxq, targets[0].Xklb); err != nil {
					recordKeepalive(false)
					logf("⚠⚠ [保活] 探测失败: %v —— 会话可能已失效，请在开闸前重新登录！", err)
					fmt.Println("⚠⚠ [保活] 会话可能已失效，请立即重新登录！")
				} else {
					recordKeepalive(true) // 正常时静默
				}
				time.Sleep(30 * time.Second)
			} else {
				time.Sleep(200 * time.Millisecond)
			}
		}
		logf("开闸！开始提交...")
	}

	// 依次抢多门
	for i, t := range targets {
		name := t.Name
		if name == "" {
			name = t.Rwh
		}
		webGrabState.mu.Lock()
		webGrabState.current = fmt.Sprintf("第 %d/%d 门：%s", i+1, len(targets), name)
		webGrabState.mu.Unlock()
		logf("=== 抢第 %d/%d 门: %s / %s ===", i+1, len(targets), t.Xklb, t.Rwh)
		res := grabCourse(webClient, xnxq, t.Xklb, t.Rwh, logf)
		ok := res == ResSuccess || res == ResDuplicate
		webGrabState.mu.Lock()
		webGrabState.results = append(webGrabState.results, grabResult{Name: name, Result: res.String(), Ok: ok})
		webGrabState.mu.Unlock()

		switch res {
		case ResSuccess, ResDuplicate:
			logf("✅ 第 %d 门选课成功", i+1)
		case ResFrozen:
			logf("⚠ 触发风控冻结！立即停止。")
			webGrabState.result = "触发风控冻结"
			return
		case ResSessionDead:
			recordKeepalive(false)
			logf("⚠⚠ 会话已失效，停止抢课。请重新登录后再试！")
			webGrabState.result = "会话已失效"
			return
		case ResStopped:
			logf("已停止，未继续后续课程")
			webGrabState.result = "已停止"
			return
		case ResCapacityFull:
			logf("第 %d 门容量已满", i+1)
		default:
			logf("第 %d 门未成功（%s）", i+1, res)
		}
		if grabStop.Load() {
			logf("已手动停止，未继续后续课程")
			webGrabState.result = "已停止"
			return
		}
	}
	webGrabState.result = "完成"
	logf("全部课程处理完成")
}

// —— 保活 ——
// defaultXnxq 返回当前年份的秋季学期（用于保活时的默认查询参数）。
func defaultXnxq() string {
	return semesterString(time.Now().Year(), 1)
}

// startKeepalive 登录后每 30 秒轻量查询保活：正常时静默（只更新状态），
// 一旦失效立即全通道醒目告警（界面横幅+日志+控制台+标题栏），并标记未登录。
func startKeepalive() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if !getLoggedIn() || webGrabState.running {
				continue
			}
			if _, err := fetchToken(webClient, defaultXnxq(), "cxsy"); err != nil {
				ts := time.Now().Format("15:04:05")
				recordKeepalive(false)
				setLoggedIn(false)
				webGrabState.addLog("⚠⚠ [保活] %s 会话已失效（%v）—— 请立即重新登录！", ts, err)
				fmt.Println("⚠⚠ [保活] 会话已失效，请立即重新登录！", err)
			} else {
				recordKeepalive(true) // 正常时静默：只刷新状态，不打日志
			}
		}
	}()
}

// —— 保活状态（界面顶部显示"最近保活时间"，失效时醒目告警）——
var keepaliveState struct {
	mu   sync.Mutex
	time time.Time
	ok   bool
	has  bool
}

func recordKeepalive(ok bool) {
	keepaliveState.mu.Lock()
	keepaliveState.time = time.Now()
	keepaliveState.ok = ok
	keepaliveState.has = true
	keepaliveState.mu.Unlock()
}

// resetKeepalive 清除保活状态（重新登录成功后调用，撤销失效告警）。
func resetKeepalive() {
	keepaliveState.mu.Lock()
	keepaliveState.has = false
	keepaliveState.mu.Unlock()
}

func keepaliveSnapshot() (timeStr string, ok bool, has bool) {
	keepaliveState.mu.Lock()
	defer keepaliveState.mu.Unlock()
	if !keepaliveState.has {
		return "", false, false
	}
	return keepaliveState.time.Format("15:04:05"), keepaliveState.ok, true
}

// —— 启动 ——
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func runWeb() {
	webClient = newClient()
	if ok, _ := loadCookies(webClient, "cookie.json"); ok {
		// 不盲目信任 cookie.json：先验证会话再显示「已登录」，
		// 否则过期 cookie 会让界面误报已登录。
		go func() {
			if _, err := fetchToken(webClient, defaultXnxq(), "cxsy"); err != nil {
				// 保存的会话已失效：静默标记未登录即可。
				// 刚打开程序还没登录，过期属正常现象，不打日志、不弹告警。
				setLoggedIn(false)
			} else {
				setLoggedIn(true)
				recordKeepalive(true)
			}
		}()
	}
	startKeepalive() // 登录后每 30 秒保活探测，失效即时告警

	// 退出时尽力释放 CAS 会话（与 CLI 模式对齐），避免僵尸会话累积到冻结阈值：
	// 1) Ctrl+C：Go 的 os/signal 可以捕获，先登出再退出；
	// 2) 点窗口叉号：Windows 发 CTRL_CLOSE_EVENT，由 console_windows.go 的
	//    SetConsoleCtrlHandler 在系统 5 秒宽限期内登出；唯一救不了的是
	//    任务管理器/强制结束这类直接强杀，见该文件注释。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		fmt.Println("\n[exit] 收到 Ctrl+C，释放 CAS 会话...")
		doLogout(webClient)
		os.Exit(0)
	}()
	setupConsoleCloseHandler(func() {
		doLogout(webClient)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/state", handleState)
	mux.HandleFunc("/api/login/qr", handleLoginQR)
	mux.HandleFunc("/api/login/qr.png", handleLoginQRImage)
	mux.HandleFunc("/api/login/status", handleLoginStatus)
	mux.HandleFunc("/api/login/confirm", handleLoginConfirm)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/course-types", handleCourseTypes)
	mux.HandleFunc("/api/semesters", handleSemesters)
	mux.HandleFunc("/api/courses", handleCourses)
	mux.HandleFunc("/api/grab", handleGrab)
	mux.HandleFunc("/api/grab/status", handleGrabStatus)
	mux.HandleFunc("/api/grab/stop", handleGrabStop)

	addr := "127.0.0.1:8080"
	go openBrowser("http://" + addr)
	fmt.Println("========================================")
	fmt.Println("HITWH 抢课 Web 界面已启动")
	fmt.Println("地址: http://" + addr)
	fmt.Println("浏览器会自动打开；若无响应请手动访问")
	fmt.Println("关闭本窗口或按 Ctrl+C 退出")
	fmt.Println("========================================")
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Println("Web 服务启动失败:", err)
	}
}
