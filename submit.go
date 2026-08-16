package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
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

// rateLimiter 限制提交节奏，防止短时间高频。
type rateLimiter struct {
	minInterval time.Duration
	mu          sync.Mutex
	last        time.Time
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	return &rateLimiter{minInterval: interval}
}

func (r *rateLimiter) wait() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.last.IsZero() {
		elapsed := time.Since(r.last)
		if elapsed < r.minInterval {
			jitter := time.Duration(rand.Int63n(r.minInterval.Nanoseconds()/4 + 1))
			time.Sleep(r.minInterval - elapsed + jitter)
		}
	}
	r.last = time.Now()
}

func isDecisive(r SubmitResult) bool {
	switch r {
	case ResSuccess, ResDuplicate, ResCapacityFull, ResIllegal, ResNotForGrade, ResFrozen, ResStopped:
		return true
	}
	// ResNotRegistered / ResNotWithinTime 是"选课未开放"的暂时状态，应重试而非停止
	return false
}

// grabCourse 串行抢课：每次先取新 token 再提交（token 单次有效，不能并发复用）。
func grabCourse(c *http.Client, xnxq, xklb, rwh string, interval time.Duration, maxRetries int, logf func(string, ...interface{})) SubmitResult {
	rl := newRateLimiter(interval)
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if grabStop.Load() {
			logf("已手动停止")
			return ResStopped
		}
		rl.wait()
		token, err := fetchToken(c, xnxq, xklb)
		if err != nil {
			logf("第%d次：取 token 失败 %v", attempt, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		res, err := submitOnce(c, xklb, xnxq, rwh, token)
		if err != nil {
			logf("第%d次：提交异常 %v", attempt, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		logf("第%d次：%s", attempt, res)
		if isDecisive(res) {
			return res
		}
		// 未开放/瞬时失败：快速重试
		time.Sleep(150 * time.Millisecond)
	}
	return ResUnknown
}
