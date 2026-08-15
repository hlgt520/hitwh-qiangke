package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Course struct {
	Code string // rwh：提交选课用的任务号
	Name string // 课程名称
	Info string // 摘要：课程代码/校区/院系/学分/已选容量
}

func semesterString(yearStart, order int) string {
	return fmt.Sprintf("%d-%d%d", yearStart, yearStart+1, order)
}

// buildXsxkForm 构造选课查询/提交共用的表单字段（与浏览器 queryform 一致）。
func buildXsxkForm(xnxq, xklb, token, rwh string) url.Values {
	form := url.Values{}
	form.Set("pageXnxq", xnxq)
	form.Set("pageXklb", xklb)
	form.Set("token", token)
	form.Set("rwh", rwh)
	form.Set("kcdmpx", "")
	form.Set("kcmcpx", "")
	form.Set("rlpx", "")
	form.Set("path_id", "")
	form.Set("zy", "")
	form.Set("qz", "")
	form.Set("pageKkxiaoqu", "")
	form.Set("pageKkyx", "")
	form.Set("pageKcmc", "")
	return form
}

// queryXsxk 完整查询，返回 HTML、响应中的新 token 与 pageCount。
func queryXsxk(c *http.Client, xnxq, xklb string, page, pageSize, pageCount int, token string) (string, string, int, error) {
	form := buildXsxkForm(xnxq, xklb, token, "")
	form.Set("pageNo", strconv.Itoa(page))
	form.Set("pageSize", strconv.Itoa(pageSize))
	if pageCount > 0 {
		form.Set("pageCount", strconv.Itoa(pageCount))
	}
	body, status, _, _, err := doPostForm(c, queryListURL, form)
	if err != nil {
		return "", "", 0, err
	}
	if status != 200 {
		return "", "", 0, fmt.Errorf("查课表状态码 %d", status)
	}
	s := string(body)
	t, pc, _ := parsePage(s)
	return s, t, pc, nil
}

// fetchCourses 获取课程列表（先取 token+pageCount，再逐页查询，返回全部课程）。
func fetchCourses(c *http.Client, xnxq, xklb string) ([]Course, string, error) {
	_, token, pc, err := queryXsxk(c, xnxq, xklb, 1, 20, 0, "")
	if err != nil {
		return nil, "", err
	}
	if pc < 1 {
		pc = 1
	}
	var all []Course
	lastToken := token
	for page := 1; page <= pc; page++ {
		html, t2, _, err := queryXsxk(c, xnxq, xklb, page, 20, pc, token)
		if err != nil {
			return nil, "", err
		}
		token = t2
		lastToken = t2
		_, _, courses := parsePage(html)
		all = append(all, courses...)
	}
	return all, lastToken, nil
}

// fetchToken 获取提交选课所需的 token（实测：单次查询拿到的 token 即可直接提交）。
func fetchToken(c *http.Client, xnxq, xklb string) (string, error) {
	_, t, _, err := queryXsxk(c, xnxq, xklb, 1, 20, 0, "")
	if err != nil {
		return "", err
	}
	if t == "" {
		return "", fmt.Errorf("未解析到 token（可能会话失效，需重新登录）")
	}
	return t, nil
}

var (
	reToken     = regexp.MustCompile(`(?is)<input[^>]*\bid=["']token["'][^>]*\bvalue=["']([^"']*)["']`)
	rePageCount = regexp.MustCompile(`(?is)<input[^>]*\bid=["']pageCount["'][^>]*\bvalue=["']([^"']*)["']`)
	reTr        = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	reTd        = regexp.MustCompile(`(?is)<td[^>]*>(.*?)</td>`)
	reInputId   = regexp.MustCompile(`(?is)<input[^>]*\bid=["']([^"']+)["']`)
	reTag       = regexp.MustCompile(`<[^>]+>`)
)

func stripTags(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return strings.Join(strings.Fields(s), " ")
}

// parsePage 从 queryXsxkList 返回的 HTML 中提取 token、pageCount 与课程列表。
func parsePage(html string) (token string, pageCount int, courses []Course) {
	if m := reToken.FindStringSubmatch(html); m != nil {
		token = m[1]
	}
	if m := rePageCount.FindStringSubmatch(html); m != nil {
		pageCount, _ = strconv.Atoi(m[1])
	}
	courses = parseCourses(html)
	return
}

func parseCourses(html string) []Course {
	var out []Course
	cell := func(tds [][]string, i int) string {
		if i < len(tds) {
			return stripTags(tds[i][1])
		}
		return ""
	}
	for _, trm := range reTr.FindAllStringSubmatch(html, -1) {
		row := trm[1]
		tds := reTd.FindAllStringSubmatch(row, -1)
		if len(tds) < 4 {
			continue
		}
		// rwh 在最后一列 <input id="xkyq_<rwh>"> 中
		m := reInputId.FindStringSubmatch(tds[len(tds)-1][1])
		if m == nil || !strings.HasPrefix(m[1], "xkyq_") {
			continue
		}
		rwh := strings.TrimPrefix(m[1], "xkyq_")
		name := cell(tds, 3)
		kcCode := cell(tds, 2)            // 课程代码
		campus := cell(tds, 6)            // 校区
		dept := cell(tds, 10)             // 开课院系
		credit := cell(tds, 11)           // 学分
		capacity := cell(tds, len(tds)-1) // 已选/容量
		info := fmt.Sprintf("课程代码=%s 校区=%s 院系=%s 学分=%s 已选/容量=%s",
			kcCode, campus, dept, credit, capacity)
		out = append(out, Course{Code: rwh, Name: name, Info: info})
	}
	return out
}
