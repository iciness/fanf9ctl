@echo off
setlocal
cd /d "%~dp0"

where go >nul 2>nul
if errorlevel 1 (
    echo [x] 没找到 go，请先安装 Go 1.21+  https://go.dev/dl/
    pause
    exit /b 1
)

echo [*] 编译中...
go build -trimpath -ldflags "-s -w -H windowsgui" -o fanf9ctl.exe .
if errorlevel 1 (
    echo [x] 编译失败
    pause
    exit /b 1
)

echo [ok] 已生成 fanf9ctl.exe
echo.
echo   直接双击 fanf9ctl.exe 即可，或先运行 fanf9ctl.exe -list 看设备。
pause
