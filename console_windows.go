//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// CTRL_CLOSE_EVENT：用户点击控制台窗口叉号（或系统关机）时，Windows 发给进程的事件。
const ctrlCloseEvent = 0x2

// setupConsoleCloseHandler 注册 Windows 控制台关闭事件处理器。
//
// 背景：点窗口叉号时 Windows 向进程发送 CTRL_CLOSE_EVENT，并给予约 5 秒宽限期，
// 超时后强制终止进程。Go 标准库的 os/signal 收不到这个事件（只能收 Ctrl+C），
// 所以此前 Web 模式点叉号关窗 = 直接被杀 = 留下僵尸 CAS 会话。
//
// 这里通过 kernel32.SetConsoleCtrlHandler 注册回调，在宽限期内尽力登出；
// 登出正常只需几百毫秒，远小于 5 秒。若网络异常导致登出卡住，最多等 4 秒，
// 之后由系统强制结束（这是 Windows 的硬性机制，任何程序都绕不过）。
//
// 注意：该回调只处理 CTRL_CLOSE_EVENT；Ctrl+C（CTRL_C_EVENT）返回 0 放行，
// 由 Go 的 signal.Notify 处理，两者不冲突。
func setupConsoleCloseHandler(fn func()) {
	callback := syscall.NewCallback(func(ctrlType uint32) uintptr {
		if ctrlType != ctrlCloseEvent {
			return 0 // 交给后续处理器 / Go 的 signal.Notify
		}
		fmt.Println("\n[exit] 检测到窗口关闭，释放 CAS 会话...")
		done := make(chan struct{})
		go func() {
			defer close(done)
			fn()
		}()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			fmt.Println("[exit] 登出超时，等待系统强制结束")
		}
		os.Exit(0)
		return 1 // 不可达（os.Exit 已终止进程），仅为满足编译/vet 对返回值的检查
	})

	dll := syscall.NewLazyDLL("kernel32.dll")
	proc := dll.NewProc("SetConsoleCtrlHandler")
	r, _, err := proc.Call(callback, 1) // AddHandler = TRUE
	if r == 0 {
		fmt.Printf("[exit] 注册窗口关闭处理器失败: %v\n", err)
	}
}
