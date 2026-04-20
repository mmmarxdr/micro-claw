package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"daimon/internal/tool"
)

// skillShellTool implements tool.Tool for a fixed-command skill tool.
// The command is determined at load time from the skill file and cannot
// be modified by the LLM at runtime.
type skillShellTool struct {
	def ToolDef // immutable after construction
}

// NewSkillShellTool constructs a skillShellTool from a ToolDef.
func NewSkillShellTool(def ToolDef) *skillShellTool {
	return &skillShellTool{def: def}
}

func (s *skillShellTool) Name() string { return s.def.Name }

func (s *skillShellTool) Description() string { return s.def.Description }

// Schema returns an empty JSON object schema.
// Skill tools accept no LLM-supplied parameters — the command is fixed.
// The LLM invokes the tool by name with no input arguments.
func (s *skillShellTool) Schema() json.RawMessage {
	return json.RawMessage(`{}`)
}

func (s *skillShellTool) Execute(ctx context.Context, _ json.RawMessage) (tool.ToolResult, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", s.def.Command)

	// Apply working directory
	if s.def.WorkingDir != "" {
		cmd.Dir = s.def.WorkingDir
	}

	// Build env: start from process environment then overlay def.Env.
	if len(s.def.Env) > 0 {
		env := os.Environ()
		for k, v := range s.def.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

	out, err := cmd.CombinedOutput()

	const maxLen = 64 * 1024
	outStr := string(out)
	if len(outStr) > maxLen {
		originalLen := len(outStr)
		outStr = outStr[:maxLen] + fmt.Sprintf("\n...(output truncated — showing first %d of %d bytes)", maxLen, originalLen)
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return tool.ToolResult{IsError: true, Content: "Tool timed out"}, nil
		}
		exitCode := "-1"
		if cmd.ProcessState != nil {
			exitCode = strconv.Itoa(cmd.ProcessState.ExitCode())
		}
		return tool.ToolResult{
			IsError: true,
			Content: fmt.Sprintf("Command failed: %v\nOutput: %s", err, outStr),
			Meta:    map[string]string{"command": s.def.Command, "exit_code": exitCode},
		}, nil
	}

	if len(outStr) == 0 {
		outStr = "(command successful, no output)"
	}

	return tool.ToolResult{
		Content: outStr,
		Meta:    map[string]string{"command": s.def.Command, "exit_code": "0"},
	}, nil
}
