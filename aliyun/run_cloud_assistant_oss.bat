@echo off
REM 编译并运行 cloud_assistant_oss.go
echo Building cloud_assistant_oss.exe...
REM 删除旧的exe文件（如果存在）
if exist cloud_assistant_oss.exe (
    echo Deleting old cloud_assistant_oss.exe...
    del cloud_assistant_oss.exe
)
go build -tags cloud_assistant_oss_main -o cloud_assistant_oss.exe .
if %ERRORLEVEL% NEQ 0 (
    echo Build failed!
    pause
    exit /b 1
)
echo Build successful!
echo.
echo Running cloud_assistant_oss.exe...
echo.
cloud_assistant_oss.exe
pause

