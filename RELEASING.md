# Releasing

This repository uses GoReleaser to build release artifacts for both binaries:

- `symterm`: client builds for Linux, macOS, and Windows on `amd64` and `arm64`
- `symtermd`: daemon builds for Linux on `amd64` and `arm64`

## Local snapshot build

```bash
goreleaser build --snapshot --clean
```

Equivalent repo scripts:

```powershell
.\tools\goreleaser_build_snapshot.ps1
```

```bash
./tools/goreleaser_build_snapshot.sh
```

This writes archives and checksums into `dist/` without publishing a release.

If the repository does not yet have a git remote, prefer this command over
`goreleaser check`. GoReleaser validates SCM release metadata during `check`,
so a repo without `origin` will fail that step even though snapshot builds work.

## Tagged release

```bash
git tag v0.1.0
git push origin v0.1.0
goreleaser release --clean
```

`goreleaser release` expects a configured remote release backend such as GitHub, GitLab, or Gitea.

## Notes

- GoReleaser runs `go test ./...` before building artifacts.
- Linux archives are emitted as `.tar.gz`.
- Windows client archives are emitted as `.zip`.
- `symtermd` is intentionally released only for Linux because the daemon requires Linux and FUSE3 for real runtime support.
