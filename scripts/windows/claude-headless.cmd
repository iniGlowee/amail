@echo off
rem claude-headless.cmd - answer a prompt from stdin with Claude Code in
rem non-interactive mode, with every tool switched off, and print the
rem answer. Used as an "amail process" handler command.
rem
rem Finds the Claude Code CLI bundled with the Claude desktop app (newest
rem version), or a "claude" on PATH. Requires a one-time login by the user
rem (run the exe once interactively and use /login).
setlocal EnableDelayedExpansion
set "EXE="
if exist "%APPDATA%\Claude\claude-code" (
  for /f "delims=" %%d in ('dir /b /ad /o-n "%APPDATA%\Claude\claude-code" 2^>nul') do (
    if not defined EXE if exist "%APPDATA%\Claude\claude-code\%%d\claude.exe" set "EXE=%APPDATA%\Claude\claude-code\%%d\claude.exe"
  )
)
if not defined EXE for %%p in (claude.exe claude.cmd claude) do if not defined EXE if not "%%~$PATH:p"=="" set "EXE=%%~$PATH:p"
if not defined EXE (
  echo Claude Code CLI not found ^(looked in %APPDATA%\Claude\claude-code and PATH^) 1>&2
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
