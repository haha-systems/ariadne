## Plan
- [ ] 1. Setup
  - [ ] 1.1. Create workpad
  - [ ] 1.2. Sync repo
- [ ] 2. Rename GitHub labels
  - [ ] 2.1. Rename `conductor` to `ariadne`
  - [ ] 2.2. Rename `conductor:claimed` to `ariadne:claimed`
  - [ ] 2.3. Rename `conductor:needs-review` to `ariadne:needs-review`
  - [ ] 2.4. Rename `conductor:reviewing` to `ariadne:reviewing`
  - [ ] 2.5. Rename `conductor:approved` to `ariadne:approved`
  - [ ] 2.6. Rename `conductor:needs-revision` to `ariadne:needs-revision`
  - [ ] 2.7. Rename `conductor:revising` to `ariadne:revising`
  - [ ] 2.8. Rename `conductor:review-abandoned` to `ariadne:review-abandoned`
  - [ ] 2.9. Rename `conductor:review-cycle-1` to `ariadne:review-cycle-1`
  - [ ] 2.10. Rename `conductor:review-cycle-2` to `ariadne:review-cycle-2`
  - [ ] 2.11. Rename `conductor:review-cycle-3` to `ariadne:review-cycle-3`
  - [ ] 2.12. Rename `conductor:rebasing` to `ariadne:rebasing`
  - [ ] 2.13. Rename `conductor:rebase-attempts-1` to `ariadne:rebase-attempts-1`
  - [ ] 2.14. Rename `conductor:rebase-attempts-2` to `ariadne:rebase-attempts-2`
  - [ ] 2.15. Rename `conductor:rebase-attempts-3` to `ariadne:rebase-attempts-3`
- [ ] 3. Update code and documentation
  - [ ] 3.1. Update `internal/worksource/github.go`
  - [ ] 3.2. Update `ariadne.toml.example`
  - [ ] 3.3. Update `README.md`
- [ ] 4. Validate
  - [ ] 4.1. Run `go test ./...`
- [ ] 5. Open PR
  - [ ] 5.1. Create new branch
  - [ ] 5.2. Commit changes
  - [ ] 5.3. Push branch
  - [ ] 5.4. Create PR
  - [ ] 5.5. Record PR URL

## Acceptance Criteria
- [ ] All `conductor:` labels are renamed to `ariadne:` in the GitHub repo.
- [ ] All references to `conductor:` labels in the code are updated to `ariadne:`.
- [ ] The `label_filter` in `ariadne.toml.example` is updated.
- [ ] `README.md` is updated.
- [ ] `go test ./...` passes.

## Validation
- [ ] `go test ./...`

## Notes
-

## Confusions
-
