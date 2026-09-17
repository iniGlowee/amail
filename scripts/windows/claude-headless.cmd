@echo off
rem claude-headless.cmd - answer a prompt from stdin with Claude Code in
rem non-interactive mode, with every tool switched off, and print the
rem answer. Used as an "amail process" handler command.
rem
rem Finds the Claude Code CLI: the native installer location
rem (irm https://claude.ai/install.ps1 | iex), the npm global location
rem (npm install -g @anthropic-ai/claude-code), the copy bundled with the
rem Claude desktop app, or a "claude" on PATH. Requires a one-time login by
rem the user (run "claude" in a terminal once and use /login).
setlocal EnableDelayedExpansion
set "EXE="
if exist "%USERPROFILE%\.local\bin\claude.exe" set "EXE=%USERPROFILE%\.local\bin\claude.exe"
if not defined EXE if exist "%APPDATA%\npm\claude.cmd" set "EXE=%APPDATA%\npm\claude.cmd"
if not defined EXE if exist "%APPDATA%\Claude\claude-code" (
  for /f "delims=" %%d in ('dir /b /ad /o-n "%APPDATA%\Claude\claude-code" 2^>nul') do (
    if not defined EXE if exist "%APPDATA%\Claude\claude-code\%%d\claude.exe" set "EXE=%APPDATA%\Claude\claude-code\%%d\claude.exe"
  )
)
if not defined EXE for %%p in (claude.exe claude.cmd claude) do if not defined EXE if not "%%~$PATH:p"=="" set "EXE=%%~$PATH:p"
if not defined EXE (
  echo Claude Code CLI not found. Looked in %USERPROFILE%\.local\bin, %APPDATA%\npm, %APPDATA%\Claude\claude-code and PATH. Install it with:  irm https://claude.ai/install.ps1 ^| iex   or   npm install -g @anthropic-ai/claude-code   then run "claude" once and /login. 1>&2
  exit /b 2
)
rem Work in an empty folder so nothing of the user's is in reach even if a
rem tool were somehow allowed. --tools "" disables every tool; no session is
rem saved to disk.
set "WORK=%TEMP%\amail-claude"
if not exist "%WORK%" mkdir "%WORK%" >nul 2>&1
cd /d "%WORK%"
"%EXE%" -p --no-session-persistence --tools "" --output-format text --max-turns 1 --append-system-prompt "You are answering a question that arrived by e-mail through AMail. You have no tools and no access to files, the network or the user's computer; answer from knowledge only. Reply in plain text (no Markdown tables or code fences unless the user asks for code), concise and complete. Do not ask follow-up questions; if something is ambiguous, state your assumption and answer."
exit /b %ERRORLEVEL%
