package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type SubmitResult int

const (
	ResUnknown SubmitResult = iota
	ResSuccess
	ResNotWithinTime
	ResNotRegistered
	ResIllegal
	ResDuplicate
	ResFailed
	ResNotForGrade
	ResCapacityFull
	ResFrozen
	ResStopped
	ResSessionDead
)

// grabStop 全局停止标志（Web 界面的「停止」按钮使用）。
var grabStop atomic.Bool

func (r SubmitResult) String() string {
	switch r {
	case ResSuccess:
		return "选课成功"
	case ResNotWithinTime:
		return "不在选课时间范围内"
	case ResNotRegistered:
		return "学生未注册/选课未开放"
	case ResIllegal:
		return "非法操作"
	case ResDuplicate:
		return "已选/重复选课"
	case ResFailed:
		return "选课失败"
	case ResNotForGrade:
		return "不在面向年级内"
	case ResCapacityFull:
		return "容量已满"
	case ResFrozen:
		return "触发风控/冻结"
	case ResStopped:
		return "手动停止"
	case ResSessionDead:
		return "会话已失效"
	}
	return "未知结果"
}

func classifySubmit(html string) SubmitResult {
	// 风控关键词优先级最高，一旦出现立即停手
	if strings.Contains(html, "频繁") || strings.Contains(html, "冻结") ||
		strings.Contains(html, "并行会话") || strings.Contains(html, "IP数") {
		return ResFrozen
	}
	switch {
	case strings.Contains(html, "选课成功"):
		return ResSuccess
	case strings.Contains(html, "未进行注册") || strings.Contains(html, "不可选课"):
		return ResNotRegistered
	case strings.Contains(html, "不在学生选课时间范围内"):
		return ResNotWithinTime
	case strings.Contains(html, "非法操作"):
		return ResIllegal
	case strings.Contains(html, "不可重复选课"):
		return ResDuplicate
	case strings.Contains(html, "不在面向年级"):
		return ResNotForGrade
	case strings.Contains(html, "容量已满"):
		return ResCapacityFull
	case strings.Contains(html, "选课失败"):
		return ResFailed
	}
	return ResUnknown
}

func submitOnce(c *http.Client, xklb, xnxq, rwh, token string) (SubmitResult, error) {
	form := buildXsxkForm(xnxq, xklb, token, rwh)
	body, status, _, _, err := doPostForm(c, submitURL, form)
	if err != nil {
		return ResUnknown, err
	}
	if status != 200 {
		return ResUnknown, fmt.Errorf("提交状态码 %d", status)
	}
	return classifySubmit(string(body)), nil
}

func isDecisive(r SubmitResult) bool {
	switch r {
	case ResSuccess, ResDuplicate, ResCapacityFull, ResNotForGrade, ResFrozen, ResStopped, ResSessionDead:
		return true
	}
	// 非法操作/选课失败/未注册/不在时间/未知 → 重试。
	// 「非法操作」通常是 token 过期（token 单次有效），下一轮重新取 token 即可，不应停止。
	return false
}

// maxTokenFails 连续取 token 失败达到该次数即判定「会话已失效」，停下来报告（不会自动重登）。
// 开闸高峰偶发单次网络失败很常见，故用连续计数而非一次失败就停。
const maxTokenFails = 5

// grabCourse 串行抢课：每次先取新 token 再提交（token 单次有效，不能并发复用）。
// 一直重试，直到「选课成功 / 已选 / 容量满 / 不在年级 / 风控 / 会话失效 / 手动停止」才返回。
// 抢课拼速度：去掉人为限速与退避，让网络往返成为唯一的节流。
func grabCourse(c *http.Client, xnxq, xklb, rwh string, logf func(string, ...interface{})) SubmitResult {
	consecTokenFails := 0
	for attempt := 1; ; attempt++ {
		if grabStop.Load() {
			logf("已手动停止")
			return ResStopped
		}
		token, err := fetchToken(c, xnxq, xklb)
		if err != nil {
			consecTokenFails++
			logf("第%d次：取 token 失败（连续 %d/%d）%v", attempt, consecTokenFails, maxTokenFails, err)
			if consecTokenFails >= maxTokenFails {
				logf("⚠ 连续 %d 次取 token 失败：会话已失效", maxTokenFails)
				return ResSessionDead
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		consecTokenFails = 0
		res, err := submitOnce(c, xklb, xnxq, rwh, token)
		if err != nil {
			logf("第%d次：提交异常 %v", attempt, err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		logf("第%d次：%s", attempt, res)
		if isDecisive(res) {
			return res
		}
		// 未开放/瞬时失败：立即重试，不人为延迟
	}
}
