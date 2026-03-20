---
name: git-helper
description: "Git workflow and status tools for daily development"
version: 1.0.0
author: marc
---

# Git Helper

Always show both staged and unstaged changes when checking status.
Prefer atomic commits with clear, imperative commit messages.
Never suggest `git push --force` to main or master branches.
When showing a log, default to `--oneline` format unless asked for details.

```yaml tool
name: git_status
description: "Show git status with current branch name"
command: "git status --short && echo '---' && git branch --show-current"
timeout: 10s
```

```yaml tool
name: git_log
description: "Show the last 10 commits in oneline format"
command: "git log --oneline -10"
timeout: 5s
```

```yaml tool
name: git_diff
description: "Show unstaged changes"
command: "git diff"
timeout: 10s
```
