@echo off
REM 编译并运行 run_ecs.go
echo Building run_ecs.exe...
REM Build the package in this directory so all relevant files (including run_ecs_main.go) are included.
go build -o run_ecs.exe .
if %ERRORLEVEL% NEQ 0 (
    echo Build failed!
    pause
    exit /b 1
)
echo Build successful!
echo.
echo Running run_ecs.exe...
echo.
run_ecs.exe
pause

