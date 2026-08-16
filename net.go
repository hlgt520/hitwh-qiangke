package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html/charset"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// —— 端点（校园网直连）——
// CAS 统一身份认证：直连 ids.hit.edu.cn。
// 教务系统：直连 jwts.hitwh.edu.cn（校内网可直达，无需 WebVPN）。
const (
	casBase  = "https://ids.hit.edu.cn/authserver"
	jwtsBase = "http://jwts.hitwh.edu.cn"
	// CAS 登录成功后回调：jwts.hitwh.edu.cn/loginCAS 会据此建立教务会话。
	serviceParam = "http%3A%2F%2Fjwts.hitwh.edu.cn%2FloginCAS"
	// serviceRaw 为未编码的 service（用于二次验证提交）。
	serviceRaw = "http://jwts.hitwh.edu.cn/loginCAS"
)

var (
	getTokenURL  = casBase + "/qrCode/getToken?ts="
	getCodeURL   = casBase + "/qrCode/getCode?uuid="
	getStatusURL = casBase + "/qrCode/getStatus.htl?ts="
	loginURL     = casBase + "/login?display=qrLogin&service=" + serviceParam
	pwdLoginURL  = casBase + "/login?service=" + serviceParam
	logoutURL    = casBase + "/logout"

	queryListURL = jwtsBase + "/xsxk/queryXsxkList"
	submitURL    = jwtsBase + "/xsxk/saveXsxk"
)

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 30 {
				return fmt.Errorf("too many redirects (%d)", len(via))
			}
			return nil
		},
	}
}

// decodeBody 依据 Content-Type 自动把响应解码为 UTF-8（处理 GBK/GB2312）。
func decodeBody(resp *http.Response) ([]byte, error) {
	r, err := charset.NewReader(resp.Body, resp.Header.Get("Content-Type"))
	if err != nil {
		r = resp.Body
	}
	return io.ReadAll(r)
}

// —— cookie 元数据记录（Go 的 Jar.Cookies() 会丢失 Domain 等信息，故自行记录以便持久化）——
var cookieMeta struct {
	sync.Mutex
	list []*http.Cookie
}

func recordCookies(resp *http.Response) {
	if resp == nil || resp.Request == nil {
		return
	}
	host := resp.Request.URL.Hostname()
	cookieMeta.Lock()
	defer cookieMeta.Unlock()
	for _, ck := range resp.Cookies() {
		if ck.Domain == "" {
			ck.Domain = host
		}
		replaced := false
		for i, e := range cookieMeta.list {
			if e.Name == ck.Name && e.Domain == ck.Domain && e.Path == ck.Path {
				cookieMeta.list[i] = ck
				replaced = true
				break
			}
		}
		if !replaced {
			cookieMeta.list = append(cookieMeta.list, ck)
		}
	}
}

func collectAllCookies() []*http.Cookie {
	cookieMeta.Lock()
	defer cookieMeta.Unlock()
	out := make([]*http.Cookie, len(cookieMeta.list))
	copy(out, cookieMeta.list)
	return out
}

func doGet(c *http.Client, u string) (body []byte, status int, finalURL string, header http.Header, err error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, 0, "", nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		if resp != nil {
			recordCookies(resp)
			final := resp.Request.URL.String()
			resp.Body.Close()
			return nil, resp.StatusCode, final, resp.Header, err
		}
		return nil, 0, "", nil, err
	}
	defer resp.Body.Close()
	recordCookies(resp)
	body, err = decodeBody(resp)
	if err != nil {
		return nil, resp.StatusCode, "", nil, err
	}
	return body, resp.StatusCode, resp.Request.URL.String(), resp.Header, nil
}

// doGetRaw 原样返回字节（用于下载二维码图片等二进制）。
func doGetRaw(c *http.Client, u string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	recordCookies(resp)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func doPostForm(c *http.Client, u string, form url.Values) (body []byte, status int, finalURL string, header http.Header, err error) {
	req, err := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, "", nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		if resp != nil {
			recordCookies(resp)
			final := resp.Request.URL.String()
			resp.Body.Close()
			return nil, resp.StatusCode, final, resp.Header, err
		}
		return nil, 0, "", nil, err
	}
	defer resp.Body.Close()
	recordCookies(resp)
	body, err = decodeBody(resp)
	if err != nil {
		return nil, resp.StatusCode, "", nil, err
	}
	return body, resp.StatusCode, resp.Request.URL.String(), resp.Header, nil
}

func saveCookies(c *http.Client, file string) error {
	data, err := json.MarshalIndent(collectAllCookies(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0600)
}

func loadCookies(c *http.Client, file string) (bool, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return false, err
	}
	var cks []*http.Cookie
	if err := json.Unmarshal(data, &cks); err != nil {
		return false, err
	}
	jar := c.Jar.(*cookiejar.Jar)
	for _, ck := range cks {
		host := strings.TrimPrefix(ck.Domain, ".")
		if host == "" {
			continue
		}
		scheme := "http"
		if ck.Secure {
			scheme = "https"
		}
		u := &url.URL{Scheme: scheme, Host: host}
		jar.SetCookies(u, []*http.Cookie{ck})
	}
	cookieMeta.Lock()
	cookieMeta.list = append([]*http.Cookie(nil), cks...)
	cookieMeta.Unlock()
	return true, nil
}

func doLogout(c *http.Client) {
	_, status, _, _, err := doGet(c, logoutURL)
	if err != nil {
		fmt.Printf("[logout] 释放 CAS 会话失败: %v\n", err)
		return
	}
	fmt.Printf("[logout] 已释放 CAS 会话 (HTTP %d)\n", status)
}
