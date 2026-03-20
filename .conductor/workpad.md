## Plan
- [ ] 1. Remove `GitHubTokenEnv` from config layer
  - [ ] 1.1 Remove field from `PersonaConfig`
  - [ ] 1.2 Remove field from `personaTOML` + TOML key
  - [ ] 1.3 Remove assignment in `discoverPersonas`
- [ ] 2. Add supervisor git authorship
  - [ ] 2.1 Make `mergeEnv` variadic
  - [ ] 2.2 Add `configureGitAuthor()` function
  - [ ] 2.3 Call `configureGitAuthor` in `Execute` after worktree creation
  - [ ] 2.4 Call `configureGitAuthor` in `executeRevise` after worktree creation
- [ ] 3. Add tests
  - [ ] 3.1 `configureGitAuthor`: sets name+email, falls back to Name, nil no-ops
  - [ ] 3.2 `mergeEnv` variadic three-map case
- [ ] 4. Run tests and validate

## Acceptance Criteria
- [ ] `GitHubTokenEnv` removed from `PersonaConfig`, `personaTOML`, `discoverPersonas`
- [ ] `configureGitAuthor()` sets git user.name/user.email in worktree
- [ ] Falls back to `persona.Name` when `DisplayName` is empty
- [ ] Called in `Execute` and `executeRevise` after worktree creation
- [ ] `mergeEnv` is variadic
- [ ] All tests pass

## Validation
- [ ] `go test ./...`
- [ ] `go test -race ./...`

## Notes
- 2026-03-20 00:29 — Starting rework from HEAD 05bf389. Closed PR #55. Scope: git authorship only, no token injection.
