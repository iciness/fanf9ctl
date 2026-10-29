//go:build windows

package main

// 界面侧的诊断日志：同时写到控制台和 exe 同目录下的 fanf9ctl-ui.log。
// 托盘/开机启动这些操作没有界面可展示，出问题时靠这个文件定位。

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

func uiLog(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	log.Printf("[界面] %s", msg)
	path := filepath.Join(exeDir(), "fanf9ctl-ui.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	_ = f.Close()
}
