---
name: kanban
description: Manage a local Kanban board for sub-tasks and project tracking. Use this when you need to break down a large task into smaller steps or track your progress across multiple files.
---

# Kanban Skill

This skill allows you to manage a local Kanban board stored in `.ariadne/kanban.json`.

## Columns
- `todo`: Tasks that need to be done.
- `in-progress`: Tasks currently being worked on.
- `done`: Completed tasks.

## Usage

### Add a card
```bash
ariadne_run_skill kanban "add 'Implement auth logic'"
```

### List cards
```bash
ariadne_run_skill kanban "list"
```

### Move a card
```bash
ariadne_run_skill kanban "move <id> in-progress"
```

### Delete a card
```bash
ariadne_run_skill kanban "delete <id>"
```
