@echo off
cd /d "%~dp0"
echo ========================================================
echo              notion-manager 本地控制台
echo ========================================================
echo WebUI 仪表盘:  http://127.0.0.1:8081/dashboard/
echo 默认管理员密码: admin
echo API 接口地址:  http://127.0.0.1:8081
echo 默认 API Key:   sk-notion-manager-default
echo.
echo 支持双协议:
echo - Anthropic Messages: http://127.0.0.1:8081/v1/messages (支持 Claude Code)
echo - OpenAI 兼容接口:    http://127.0.0.1:8081/v1/chat/completions
echo.
echo Chrome 插件目录: %~dp0chrome-extension
echo ========================================================
echo 正在启动服务...
notion-manager.exe
pause
