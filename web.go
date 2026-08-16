package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
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
)

type grabState struct {
	mu      sync.Mutex
	running bool
	done    bool
	result  string
	log     []string
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

func (s *grabState) snapshot() (running, done bool, result string, log []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.done, s.result, append([]string(nil), s.log...)
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
	running, done, result, log := webGrabState.snapshot()
	writeJSON(w, map[string]interface{}{
		"loggedIn": getLoggedIn(),
		"running":  running,
		"done":     done,
		"result":   result,
		"log":      log,
	})
}

// —— 登录 ——
func handleLoginQR(w http.ResponseWriter, r *http.Request) {
	uuid, err := getQRUUID(webClient)
	if err != nil {
		writeErr(w, 500, "获取二维码失败: "+err.Error())
		return
	}
	png, err := getQRCodePNG(webClient, uuid)
	if err != nil {
		writeErr(w, 500, "生成二维码失败: "+err.Error())
		return
	}
	qrCacheMu.Lock()
	qrCache[uuid] = png
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
	if err := doCASLogin(webClient, req.Uuid); err != nil {
		writeErr(w, 500, "登录失败: "+err.Error())
		return
	}
	if err := saveCookies(webClient, "cookie.json"); err != nil {
		writeErr(w, 500, "保存会话失败: "+err.Error())
		return
	}
	setLoggedIn(true)
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
	webGrabState.log = nil
	webGrabState.mu.Unlock()

	go webGrabRun(req.Xnxq, req.Targets, req.Trigger)
	writeJSON(w, map[string]bool{"ok": true})
}

func handleGrabStatus(w http.ResponseWriter, r *http.Request) {
	running, done, result, log := webGrabState.snapshot()
	writeJSON(w, map[string]interface{}{
		"running": running,
		"done":    done,
		"result":  result,
		"log":     log,
	})
}

func webGrabRun(xnxq string, targets []Target, trigger string) {
	logf := webGrabState.addLog
	defer func() {
		if rec := recover(); rec != nil {
			logf("程序异常: %v", rec)
		}
		webGrabState.mu.Lock()
		webGrabState.running = false
		webGrabState.done = true
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
		for {
			serverNow := time.Now().Add(offset)
			if !serverNow.Before(target) {
				break
			}
			remain := target.Sub(serverNow)
			if remain > 30*time.Second {
				if _, err := fetchToken(webClient, xnxq, targets[0].Xklb); err != nil {
					logf("[保活] 失败: %v", err)
				} else {
					logf("[保活] 会话正常，距开闸 %v", remain.Round(time.Second))
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
		logf("=== 抢第 %d/%d 门: %s / %s ===", i+1, len(targets), t.Xklb, t.Rwh)
		res := grabCourse(webClient, xnxq, t.Xklb, t.Rwh, 150*time.Millisecond, 300, logf)
		switch res {
		case ResSuccess, ResDuplicate:
			logf("✅ 第 %d 门选课成功", i+1)
		case ResFrozen:
			logf("⚠ 触发风控冻结！立即停止。")
			webGrabState.result = "触发风控冻结"
			return
		case ResCapacityFull:
			logf("第 %d 门容量已满", i+1)
		default:
			logf("第 %d 门未成功（%s）", i+1, res)
		}
	}
	webGrabState.result = "完成"
	logf("全部课程处理完成")
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
		setLoggedIn(true)
	}

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
