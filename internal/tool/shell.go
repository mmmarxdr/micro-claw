package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"daimon/internal/config"
)

type ShellTool struct {
	config config.ShellToolConfig
}

func NewShellTool(cfg config.ShellToolConfig) *ShellTool {
	return &ShellTool{config: cfg}
}

func (t *ShellTool) Name() string {
	return "shell_exec"
}

func (t *ShellTool) Description() string {
	desc := "Execute a shell command on the host system. Only whitelisted commands are allowed unless allow_all is true in config."
	if t.config.WorkingDir != "" {
		desc += fmt.Sprintf(" Working directory: %s.", t.config.WorkingDir)
	}
	return desc
}

func (t *ShellTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": { "type": "string", "description": "The command to execute (e.g., 'ls -la /tmp')" }
  },
  "required": ["command"]
}`)
}

type shellParams struct {
	Command string `json:"command"`
}

func (t *ShellTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input shellParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{}, fmt.Errorf("parsing params: %w", err)
	}

	cmdStr := strings.TrimSpace(input.Command)
	if cmdStr == "" {
		return ToolResult{IsError: true, Content: "command cannot be empty"}, nil
	}

	parts := strings.Fields(cmdStr)
	baseCmd := parts[0]

	if !t.config.AllowAll {
		allowed := false
		for _, ac := range t.config.AllowedCommands {
			if ac == baseCmd {
				allowed = true
				break
			}
		}
		if !allowed {
			return ToolResult{IsError: true, Content: fmt.Sprintf("Command '%s' is not in the allowed list", baseCmd)}, nil
		}
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	if t.config.WorkingDir != "" {
		wd := t.config.WorkingDir
		if strings.HasPrefix(wd, "~") {
			if usr, err := user.Current(); err == nil {
				wd = strings.Replace(wd, "~", usr.HomeDir, 1)
			}
		}
		cmd.Dir = wd
	}

	out, err := cmd.CombinedOutput()

	const maxLen = 10 * 1024
	outStr := string(out)
	if len(outStr) > maxLen {
		outStr = outStr[:maxLen] + "\n...(output truncated)"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ToolResult{IsError: true, Content: "Tool timed out", Meta: map[string]string{"command": cmdStr, "exit_code": "-1"}}, nil
		}
		exitCode := "-1"
		if cmd.ProcessState != nil {
			exitCode = strconv.Itoa(cmd.ProcessState.ExitCode())
		}
		return ToolResult{IsError: true, Content: fmt.Sprintf("Command failed: %v\nOutput: %s", err, outStr), Meta: map[string]string{"command": cmdStr, "exit_code": exitCode}}, nil
	}

	if len(outStr) == 0 {
		outStr = "(command successful, no output)"
	}

	return ToolResult{Content: outStr, Meta: map[string]string{"command": cmdStr, "exit_code": "0"}}, nil
}
