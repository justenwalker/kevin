---
name: tidy-commits
description: Regroup unpushed commits into verified logical commits
---
1. Run `git log --oneline @{u}..HEAD` and `git status`. If the tree is dirty, stop and report.
2. Propose a grouping of the unpushed commits into logical units. Wait for approval.
3. Interactive-rebase into that grouping. Stage files explicitly by path, never `git add .`.
4. After EACH resulting commit: `go build ./...`, `go test ./...`, and the lint target must pass. Fix in place if not.
5. Rewrite messages: subject = behavior change, body = why. No em-dashes.
6. Print the final `git log --oneline @{u}..HEAD`.
