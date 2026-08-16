package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	cookieFile    = flag.String("c", "cookie.json", "cookie 持久化文件")
	rwhFlag       = flag.String("rwh", "", "课程代码 rwh（跳过交互选课）")
	xklbFlag      = flag.String("xklb", "", "选课类别 yy/ty/szhx/cxyx/cxsy/cxcy/xsyt/tsk/xsxk/sxw")
	targetsFlag   = flag.String("targets", "", "多课程目标，格式 xklb:rwh,xklb:rwh 如 cxyx:xxx,cxsy:yyy")
	xnxqFlag      = flag.String("xnxq", "", "学年学期，如 2026-20271")
	triggerFlag   = flag.String("t", "", "选课开始时间，如 2026-08-25 12:30:00（留空=立即提交）")
	intervalFlag  = flag.Int("interval", 150, "提交最小间隔(毫秒)")
	reloginFlag   = flag.Bool("relogin", false, "强制重新登录")
	loginOnlyFlag = flag.Bool("login-only", false, "仅登录并保存 cookie 后退出")
	keepAliveSec  = flag.Int("keepalive", 30, "等待开闸期间的保活间隔(秒)")
	accountFile   = flag.String("account", "account.json", "账号密码配置文件")
	setAccountFlg = flag.Bool("set-account", false, "交互式设置账号密码")
	loginModeFlag = flag.String("login", "auto", "登录方式 auto/qr/pw")
	webFlag       = flag.Bool("web", true, "启动 Web 界面（默认；加 -web=false 用命令行）")
)

var courseTypeList = []struct{ key, label string }{
	{"yy", "外语"}, {"ty", "体育"}, {"szhx", "素质核心"}, {"cxyx", "创新研修"},
	{"cxsy", "创新实验"}, {"cxcy", "创新创业"}, {"xsyt", "新生研讨"}, {"tsk", "未来技术"},
	{"xsxk", "外专业课程"}, {"sxw", "辅修"},
}

// Target 一门待抢课程。
type Target struct {
	Xklb string `json:"xklb"`           // 选课类别
	Rwh  string `json:"rwh"`            // 课程任务号
	Name string `json:"name,omitempty"` // 课程名（显示用）
}

// parseTargets 解析 -targets 参数：xklb:rwh,xklb:rwh,...
func parseTargets(s string) []Target {
	var out []Target
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
			continue
		}
		out = append(out, Target{Xklb: strings.TrimSpace(kv[0]), Rwh: strings.TrimSpace(kv[1])})
	}
	return out
}

// stdin 用单一共享 reader，避免多次 new 导致缓冲丢失。
var stdin = bufio.NewReader(os.Stdin)

func ask(prompt string) string {
	fmt.Print(prompt)
	s, _ := stdin.ReadString('\n')
	return strings.TrimSpace(s)
}

// pause 让控制台窗口在程序结束后保持，用户按回车再关闭。
func pause() {
	fmt.Print("\n按回车键退出...")
	stdin.ReadString('\n')
}

func fatalf(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
	pause()
	os.Exit(1)
}

func qrLogin(c *http.Client, cookieFile string) error {
	uuid, err := getQRUUID(c)
	if err != nil {
		return err
	}
	qrPath := "login_qrcode.png"
	if err := fetchQRCodePNG(c, uuid, qrPath); err != nil {
		return err
	}
	abs, _ := filepath.Abs(qrPath)
	fmt.Println("二维码文件（绝对路径）:", abs)
	_ = exec.Command("cmd", "/c", "start", "", abs).Start()
	fmt.Println("请用 HIT APP / 企业微信 扫描二维码并在手机上确认...")
	for {
		st, err := getQRStatus(c, uuid)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		switch st {
		case "1":
			fmt.Println("扫码已确认，正在登录教务系统...")
			if err := doCASLogin(c, uuid); err != nil {
				return err
			}
			if err := saveCookies(c, cookieFile); err != nil {
				return err
			}
			fmt.Println("登录成功，cookie 已保存到", cookieFile)
			for _, ck := range collectAllCookies() {
				fmt.Printf("  [cookie] %s = %s (domain=%s)\n", ck.Name, ck.Value, ck.Domain)
			}
			return nil
		case "2":
			fmt.Println("已扫描，请在手机上点击确认...")
		case "3":
			return fmt.Errorf("二维码已过期，请重试")
		}
		time.Sleep(time.Second)
	}
}

func chooseSemester() string {
	s := ask("请输入当前年份（如 2026）: ")
	ys, _ := strconv.Atoi(s)
	if ys < 2000 {
		ys = time.Now().Year()
	}
	fmt.Println("学期: 1) 秋季  2) 春季  3) 夏季")
	o := ask("请输入 1/2/3: ")
	order, _ := strconv.Atoi(o)
	if order < 1 || order > 3 {
		order = 1
	}
	if order != 1 {
		ys -= 1
	}
	xnxq := semesterString(ys, order)
	fmt.Println("学年学期 =", xnxq)
	return xnxq
}

func chooseCourseType() string {
	fmt.Println("选择课程类别:")
	for i, t := range courseTypeList {
		fmt.Printf("  %2d) %s (%s)\n", i+1, t.label, t.key)
	}
	s := ask("输入序号: ")
	n, _ := strconv.Atoi(s)
	if n < 1 || n > len(courseTypeList) {
		n = 1
	}
	return courseTypeList[n-1].key
}

func chooseCourse(c *http.Client, xnxq, xklb string) string {
	var courses []Course
	for {
		fmt.Println("正在拉取课程列表...")
		var err error
		courses, _, err = fetchCourses(c, xnxq, xklb)
		if err == nil && len(courses) > 0 {
			break
		}
		if err != nil {
			fmt.Println("拉取课程失败:", err)
		} else {
			fmt.Println("未解析到课程")
		}
		s := ask("会话可能已失效，是否重新登录？(y/n，默认 y): ")
		if strings.ToLower(s) == "n" || strings.ToLower(s) == "no" {
			fatalf("已取消")
		}
		if err := doLogin(c, *cookieFile); err != nil {
			fatalf("重新登录失败: %v", err)
		}
	}
	fmt.Printf("该类别共 %d 门课\n", len(courses))

	// 关键词过滤（可选）
	if kw := ask("输入关键词过滤课程名/代码（直接回车显示全部）: "); kw != "" {
		var filtered []Course
		for _, co := range courses {
			if strings.Contains(co.Name, kw) || strings.Contains(co.Code, kw) {
				filtered = append(filtered, co)
			}
		}
		if len(filtered) > 0 {
			courses = filtered
		} else {
			fmt.Println("无匹配课程，显示全部")
		}
	}

	fmt.Printf("共 %d 门可选:\n", len(courses))
	for i, co := range courses {
		info := []rune(co.Info)
		if len(info) > 90 {
			info = append(info[:90], []rune("...")...)
		}
		fmt.Printf("  %2d) %s | %s\n", i+1, co.Name, string(info))
		fmt.Printf("      rwh = %s\n", co.Code)
	}
	s := ask("输入课程序号: ")
	n, _ := strconv.Atoi(s)
	if n < 1 || n > len(courses) {
		fatalf("序号无效")
	}
	return courses[n-1].Code
}

// chooseTargets 交互式选择多门课程（可跨类别）。
func chooseTargets(c *http.Client, xnxq string) []Target {
	var targets []Target
	for {
		fmt.Printf("\n—— 添加第 %d 门课 ——\n", len(targets)+1)
		xklb := chooseCourseType()
		rwh := chooseCourse(c, xnxq, xklb)
		targets = append(targets, Target{Xklb: xklb, Rwh: rwh})
		fmt.Printf("✅ 已加入: xklb=%s rwh=%s（当前共 %d 门）\n", xklb, rwh, len(targets))
		s := ask("继续添加下一门课吗？(y/n，默认 n): ")
		if strings.ToLower(s) != "y" && strings.ToLower(s) != "yes" {
			break
		}
	}
	return targets
}

// parseTriggerTime 解析开闸时间：支持绝对时间（兼容单/双位月日）或相对时间 "+30s"/"+5m"。
func parseTriggerTime(s string) (time.Time, error) {
	if strings.HasPrefix(s, "+") {
		d, err := time.ParseDuration(s[1:])
		if err != nil {
			return time.Time{}, fmt.Errorf("相对时间格式错误（如 +30s、+5m）: %v", err)
		}
		return time.Now().Add(d), nil
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-1-2 15:04:05",
		"2006-01-02 15:04",
		"2006-1-2 15:04",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("时间格式错误（应为 2006-01-02 15:04:05，如 2026-08-15 22:09:00）")
}

func measureClockOffset(c *http.Client) time.Duration {
	_, _, _, header, err := doGet(c, jwtsBase+"/")
	if err != nil || header.Get("Date") == "" {
		return 0
	}
	t, err := http.ParseTime(header.Get("Date"))
	if err != nil {
		return 0
	}
	return t.Sub(time.Now())
}

func runGrab(c *http.Client, xnxq, xklb, rwh string) {
	logf := func(f string, a ...interface{}) {
		ts := time.Now().Format("15:04:05.000")
		fmt.Printf("[%s] %s\n", ts, fmt.Sprintf(f, a...))
	}
	res := grabCourse(c, xnxq, xklb, rwh, time.Duration(*intervalFlag)*time.Millisecond, logf)
	switch res {
	case ResSuccess, ResDuplicate:
		fmt.Println("✅ 选课成功，停止。")
	case ResFrozen:
		fmt.Println("⚠ 触发风控冻结！立即停止全部请求。冻结约 3 分钟自动恢复，程序退出。")
	case ResCapacityFull:
		fmt.Println("课程容量已满，停止。")
	case ResIllegal:
		fmt.Println("非法操作（token 过期/会话失效？可加 -relogin 重试）。")
	case ResNotForGrade:
		fmt.Println("不在面向年级内，不可选。")
	case ResStopped:
		fmt.Println("已手动停止。")
	}
}

// doLogin 统一登录：按 -login 模式选择账号密码或扫码。
func doLogin(c *http.Client, cookieFile string) error {
	mode := *loginModeFlag
	if mode == "auto" {
		if acc, err := loadAccount(*accountFile); err == nil && acc.Username != "" && acc.Password != "" {
			mode = "pw"
		} else {
			mode = "qr"
		}
	}
	switch mode {
	case "pw":
		acc, err := loadAccount(*accountFile)
		if err != nil || acc.Username == "" {
			return fmt.Errorf("未配置账号密码，请先运行 -set-account")
		}
		fmt.Println("使用账号密码登录...")
		if err := passwordLogin(c, acc.Username, acc.Password); err != nil {
			return err
		}
		return saveCookies(c, cookieFile)
	default:
		return qrLogin(c, cookieFile)
	}
}

func main() {
	flag.Parse()

	// Web 界面模式：默认（无任何命令行抢课参数时）
	cliOps := *xnxqFlag != "" || *rwhFlag != "" || *targetsFlag != "" || *xklbFlag != "" ||
		*loginOnlyFlag || *setAccountFlg || *reloginFlag || *triggerFlag != ""
	if *webFlag && !cliOps {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("\n[程序异常] %v\n", r)
				pause()
			}
		}()
		runWeb()
		return
	}

	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("\n[程序异常，已捕获] %v\n", r)
			pause()
		}
	}()
	mainBody()
	pause()
}

func mainBody() {
	client := newClient()

	// Ctrl+C 时尽量释放 CAS 会话，避免僵尸会话累积触发冻结
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		fmt.Println("\n[exit] 收到中断，释放会话...")
		doLogout(client)
		os.Exit(0)
	}()

	// —— 设置账号密码 ——
	if *setAccountFlg {
		username := ask("请输入学号: ")
		password := ask("请输入密码: ")
		if username == "" || password == "" {
			fatalf("账号或密码为空")
		}
		if err := saveAccount(*accountFile, Account{Username: username, Password: password}); err != nil {
			fatalf("保存账号失败: %v", err)
		}
		fmt.Println("账号已保存到", *accountFile)
		return
	}

	// —— 登录（默认复用已保存 cookie，登录一次即可，避免会话累积）——
	if !*reloginFlag {
		if ok, _ := loadCookies(client, *cookieFile); ok {
			fmt.Println("已加载保存的 cookie，直接复用。")
		} else if err := doLogin(client, *cookieFile); err != nil {
			fatalf("登录失败: %v", err)
		}
	} else {
		if err := doLogin(client, *cookieFile); err != nil {
			fatalf("登录失败: %v", err)
		}
	}

	// —— 仅登录模式 ——
	if *loginOnlyFlag {
		fmt.Println("登录完成，cookie 已保存（本次不登出，供后续复用）。")
		return
	}

	// —— 确定目标（支持多课程、跨类别）——
	xnxq := *xnxqFlag
	if xnxq == "" {
		xnxq = chooseSemester()
	}

	var targets []Target
	switch {
	case *targetsFlag != "":
		targets = parseTargets(*targetsFlag)
		if len(targets) == 0 {
			fatalf("-targets 格式错误，应为 xklb:rwh,xklb:rwh")
		}
	case *rwhFlag != "":
		xklb := *xklbFlag
		if xklb == "" {
			xklb = chooseCourseType()
		}
		targets = []Target{{Xklb: xklb, Rwh: *rwhFlag}}
	default:
		// 交互式多课程（可跨类别）
		targets = chooseTargets(client, xnxq)
		// 询问是否定时
		if *triggerFlag == "" {
			s := ask("\n是否定时抢课？输入开闸时间(2006-01-02 15:04:05)或直接回车立即抢: ")
			if s != "" {
				*triggerFlag = s
			}
		}
	}

	fmt.Printf("\n目标共 %d 门（按顺序抢）:\n", len(targets))
	for i, t := range targets {
		fmt.Printf("  %d) xklb=%s  rwh=%s\n", i+1, t.Xklb, t.Rwh)
	}

	// —— 触发时间（定时等待 + 保活）——
	if *triggerFlag != "" {
		target, err := parseTriggerTime(*triggerFlag)
		if err != nil {
			fatalf("%v", err)
		}
		offset := measureClockOffset(client)
		fmt.Printf("服务器时钟偏移约 %v\n", offset)
		fmt.Printf("目标开闸时间(服务器) = %s\n", target.Format("2006-01-02 15:04:05"))
		for {
			serverNow := time.Now().Add(offset)
			if !serverNow.Before(target) {
				break
			}
			remain := target.Sub(serverNow)
			if remain > time.Duration(*keepAliveSec)*time.Second {
				if _, err := fetchToken(client, xnxq, targets[0].Xklb); err != nil {
					fmt.Println("[keepalive] 失败:", err)
				} else {
					fmt.Printf("[keepalive] %s 会话正常，距开闸 %v\n",
						serverNow.Format("15:04:05"), remain.Round(time.Second))
				}
				time.Sleep(time.Duration(*keepAliveSec) * time.Second)
			} else {
				time.Sleep(100 * time.Millisecond)
			}
		}
		fmt.Println("开闸！开始提交...")
	}

	// —— 依次抢多门 ——
	for i, t := range targets {
		fmt.Printf("\n=== 抢第 %d/%d 门: xklb=%s rwh=%s ===\n", i+1, len(targets), t.Xklb, t.Rwh)
		runGrab(client, xnxq, t.Xklb, t.Rwh)
		if i < len(targets)-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	doLogout(client)
}
