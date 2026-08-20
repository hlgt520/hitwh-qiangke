package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func getQRUUID(c *http.Client) (string, error) {
	u := getTokenURL + strconv.FormatInt(time.Now().UnixMilli(), 10)
	body, status, _, _, err := doGet(c, u)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("getToken 状态码 %d", status)
	}
	return strings.TrimSpace(string(body)), nil
}

func getQRCodePNG(c *http.Client, uuid string) ([]byte, error) {
	u := getCodeURL + uuid
	body, status, err := doGetRaw(c, u)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("getCode 状态码 %d", status)
	}
	return body, nil
}

func fetchQRCodePNG(c *http.Client, uuid, file string) error {
	body, err := getQRCodePNG(c, uuid)
	if err != nil {
		return err
	}
	return os.WriteFile(file, body, 0644)
}

func getQRStatus(c *http.Client, uuid string) (string, error) {
	u := getStatusURL + strconv.FormatInt(time.Now().UnixMilli(), 10) + "&uuid=" + uuid
	body, status, _, _, err := doGet(c, u)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("getStatus 状态码 %d", status)
	}
	return strings.TrimSpace(string(body)), nil
}

var reExecution = regexp.MustCompile(`name="execution"[^>]*value="([^"]*)"`)

// getLoginExecution 在生成二维码时提前获取登录页的 execution（避免扫码确认后重定向拿不到）。
func getLoginExecution(c *http.Client) (string, error) {
	body, status, _, _, err := doGet(c, loginURL)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("登录页状态码 %d", status)
	}
	m := reExecution.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("未找到 execution 字段")
	}
	return string(m[1]), nil
}

// errMFARequired 表示登录触发了二次验证（调用方需据此进入 MFA 流程）。
var errMFARequired = fmt.Errorf("MFA_REQUIRED")

// lastMFAPage 保存最近一次触发 MFA 的二次验证页 HTML（供 completeMFA 解析账号）。
var lastMFAPage []byte

// doCASLogin 完成 CAS 扫码登录。
// 注意：execution 必须在扫码确认后【立即】重新获取再提交——
// 提前获取的 execution 会过期，导致 CAS 把登录判定为可疑、跳转二次验证页（教务会话建立失败）。
// 若触发二次验证，返回 errMFARequired，调用方调用 completeMFA 完成验证。
func doCASLogin(c *http.Client, uuid string) error {
	body, status, _, _, err := doGet(c, loginURL)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("登录页状态码 %d", status)
	}
	m := reExecution.FindSubmatch(body)
	if m == nil {
		return fmt.Errorf("未找到 execution 字段")
	}
	execution := string(m[1])

	form := url.Values{}
	form.Set("lt", "")
	form.Set("uuid", uuid)
	form.Set("cllt", "qrLogin")
	form.Set("dllt", "generalLogin")
	form.Set("_eventId", "submit")
	form.Set("rmShown", "1")
	form.Set("execution", execution)

	postBody, status, finalURL, _, err := doPostForm(c, loginURL, form)
	if err != nil {
		if strings.Contains(err.Error(), "redirect") {
			fmt.Println("[login] 重定向链较长（CAS→教务，属正常）:", err)
		} else {
			return err
		}
	}
	if status >= 400 {
		return fmt.Errorf("登录提交状态码 %d", status)
	}
	fmt.Println("[login] CAS 登录提交完成, 最终跳转:", finalURL)
	if strings.Contains(finalURL, "authserver/login") {
		return fmt.Errorf("登录未成功（仍停留在 CAS 登录页），最终跳转: %s", finalURL)
	}
	if strings.Contains(finalURL, "reAuthCheck") || strings.Contains(finalURL, "isMultifactor") {
		fmt.Println("账号需要二次验证，进入二次验证流程...")
		lastMFAPage = append([]byte(nil), postBody...)
		return errMFARequired
	}
	return nil
}

// extractValue 从 HTML 中提取指定 id 的 input 的 value。
func extractValue(html, id string) string {
	re := regexp.MustCompile(`(?is)<input[^>]*\bid=["']` + regexp.QuoteMeta(id) + `["'][^>]*>`)
	tag := re.FindString(html)
	if tag == "" {
		return ""
	}
	vm := regexp.MustCompile(`(?is)\bvalue=["']([^"']*)["']`).FindStringSubmatch(tag)
	if vm == nil {
		return ""
	}
	return vm[1]
}

// checkNeedCaptcha 查询该账号是否需要验证码。
func checkNeedCaptcha(c *http.Client, username string) bool {
	u := casBase + "/checkNeedCaptcha.htl?username=" + url.QueryEscape(username)
	body, status, _, _, err := doGet(c, u)
	if err != nil || status != 200 {
		return false
	}
	return strings.Contains(string(body), `"isNeed":true`)
}

// downloadCaptcha 下载验证码图片。
func downloadCaptcha(c *http.Client, file string) error {
	u := casBase + "/getCaptcha.htl?" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	body, status, err := doGetRaw(c, u)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("验证码状态码 %d", status)
	}
	return os.WriteFile(file, body, 0644)
}

// passwordLogin 账号密码登录。
func passwordLogin(c *http.Client, username, password string) error {
	body, status, _, _, err := doGet(c, pwdLoginURL)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("登录页状态码 %d", status)
	}
	s := string(body)
	salt := extractValue(s, "pwdEncryptSalt")
	execution := extractValue(s, "execution")

	captcha := ""
	if checkNeedCaptcha(c, username) {
		captchaPath := "captcha.png"
		if err := downloadCaptcha(c, captchaPath); err != nil {
			return fmt.Errorf("获取验证码失败: %v", err)
		}
		abs, _ := filepath.Abs(captchaPath)
		fmt.Println("需要验证码，图片已保存到:", abs)
		_ = exec.Command("cmd", "/c", "start", "", abs).Start()
		captcha = ask("请输入验证码: ")
	}

	encPwd := encryptPassword(password, salt)

	form := url.Values{}
	form.Set("username", username)
	form.Set("password", encPwd)
	form.Set("captcha", captcha)
	form.Set("cllt", "userNameLogin")
	form.Set("dllt", "generalLogin")
	form.Set("lt", "")
	form.Set("execution", execution)
	form.Set("_eventId", "submit")
	form.Set("rememberMe", "true")

	postBody, status, finalURL, _, err := doPostForm(c, pwdLoginURL, form)
	if err != nil {
		if strings.Contains(err.Error(), "redirect") {
			fmt.Println("[login] 重定向链较长（CAS→教务，属正常）:", err)
		} else {
			return err
		}
	}
	if status >= 400 {
		return fmt.Errorf("登录状态码 %d", status)
	}
	fmt.Println("[login] 密码登录提交完成, 最终跳转:", finalURL)
	if strings.Contains(finalURL, "authserver/login") {
		return fmt.Errorf("登录失败（用户名/密码错误）")
	}
	if strings.Contains(finalURL, "reAuthCheck") || strings.Contains(finalURL, "isMultifactor") {
		fmt.Println("账号需要二次验证，正在进入二次验证流程...")
		return completeMFA(c, string(postBody))
	}
	return nil
}

// extractReAuthParam 从二次验证页的内联 reAuthParams JSON 中提取字段。
func extractReAuthParam(html, key string) string {
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:\s*"([^"]*)"`)
	m := re.FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	return strings.ReplaceAll(m[1], `\/`, `/`)
}

// completeMFA 完成二次验证（使用手机短信验证码，reAuthType=3）。
// 账号从二次验证页的 reAuthParams 中解析，扫码/密码登录均可复用。
func completeMFA(c *http.Client, mfaHTML string) error {
	reAuthType := "3" // 手机短信验证码
	username := extractReAuthParam(mfaHTML, "reAuthUserId")
	if username == "" {
		return fmt.Errorf("未从二次验证页解析到账号")
	}
	fmt.Printf("[mfa] 二次验证方式：手机短信验证码 (reAuthType=%s, user=%s)\n", reAuthType, username)

	// 1. 切换二次验证方式到短信
	sw := url.Values{}
	sw.Set("isMultifactor", "true")
	sw.Set("reAuthType", reAuthType)
	sw.Set("service", serviceRaw)
	sb, _, _, _, err := doPostForm(c, casBase+"/reAuthCheck/changeReAuthType.do", sw)
	if err != nil {
		return err
	}
	fmt.Println("[mfa] 切换方式响应:", string(sb))

	// 2. 询问是否发送短信验证码（用户可选择不发送）
	s := ask("是否发送短信验证码到手机进行验证？(y/n，默认 y): ")
	if strings.ToLower(s) == "n" || strings.ToLower(s) == "no" {
		return fmt.Errorf("用户取消二次验证，登录未完成")
	}

	// 3. 触发短信验证码（type 3 = reAuthDynamicCodeType）
	trigger := url.Values{}
	trigger.Set("userName", username)
	trigger.Set("authCodeTypeName", "reAuthDynamicCodeType")
	tb, _, _, _, err := doPostForm(c, casBase+"/dynamicCode/getDynamicCodeByReauth.do", trigger)
	if err != nil {
		return err
	}
	fmt.Println("[mfa] 验证码触发响应:", string(tb))
	fmt.Println("短信验证码已发送到手机，请查看")

	code := ask("请输入手机收到的短信验证码: ")
	if code == "" {
		return fmt.Errorf("验证码为空")
	}

	// 4. 提交验证码（skipTmpReAuth=false = 仅本次登录。
	//    实测：false 才能完成登录直达教务系统；true（信任此设备）会让 /login 又跳回二次验证页。）
	if err := submitMFA(c, code, "false"); err != nil {
		return err
	}

	// 5. 完成后 GET /login 完成登录
	_, status3, finalURL, _, err := doGet(c, casBase+"/login?service="+serviceParam)
	if err != nil {
		if strings.Contains(err.Error(), "redirect") {
			fmt.Println("[mfa] 重定向链较长（属正常）:", err)
		} else {
			return err
		}
	}
	fmt.Printf("[mfa] /login 状态=%d 最终跳转=%s 又触发二次验证=%v\n",
		status3, finalURL, strings.Contains(finalURL, "reAuthCheck") || strings.Contains(finalURL, "isMultifactor"))
	return nil
}

// submitMFA 提交二次验证（字段与浏览器 doLogin 的 loginParams 完全一致）。
func submitMFA(c *http.Client, code, skipTmpReAuth string) error {
	form := url.Values{}
	form.Set("service", serviceRaw)
	form.Set("reAuthType", "3")
	form.Set("isMultifactor", "true")
	form.Set("password", "")
	form.Set("dynamicCode", code)
	form.Set("uuid", "")
	form.Set("answer1", "")
	form.Set("answer2", "")
	form.Set("otpCode", "")
	form.Set("skipTmpReAuth", skipTmpReAuth)
	body, status, _, hdr, err := doPostForm(c, casBase+"/reAuthCheck/reAuthSubmit.do", form)
	if err != nil {
		return err
	}
	fmt.Printf("[mfa] 提交响应(status=%d): %s\n", status, string(body))
	if sc := hdr.Get("Set-Cookie"); sc != "" {
		fmt.Println("[mfa] 提交 Set-Cookie:", sc)
	}
	if strings.Contains(string(body), "reAuth_failed") || strings.Contains(string(body), "reAuth_unauthorized") {
		return fmt.Errorf("二次验证失败：%s", string(body))
	}
	return nil
}
