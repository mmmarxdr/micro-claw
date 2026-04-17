# Skills

Skills are Markdown files that inject knowledge into the system prompt and
register shell tools on demand. They are the simplest way to extend the
agent without touching Go code.

## Anatomy of a skill

````markdown
---
name: git-helper
description: Git workflow assistant
version: 1.0.0
---

You are an expert at Git workflows. Prefer rebase over merge.

```yaml tool
name: git_log_pretty
description: Show recent commits
command: git log --oneline --graph -20
timeout: 10s
```
````

The frontmatter declares the skill. The body is prompt text that gets
injected when the skill loads. Each `yaml tool` block registers a shell
tool the agent can call.

## Installing and managing skills

```bash
microagent skills add https://example.com/react-patterns.md
microagent skills list
microagent skills info <name>
microagent skills remove <name>
```

## Tool priority

```
built-in > skill > MCP
```

A skill cannot shadow a built-in tool. An MCP tool cannot shadow a skill.

## Loading on demand

Skills are loaded via the `load_skill` tool — they do not consume context
tokens until the LLM decides a skill is relevant. This keeps the base
system prompt small.

## Config

```yaml
skills:
  skills: []                         # explicit skill file paths
  skills_dir: ~/.microagent/skills   # directory where installed skills live
```
