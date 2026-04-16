# tbox Phase 1 Limitations

These are NOT bugs — they are deliberate scope decisions or platform constraints.

## Platform Constraints (Android)
- **No kernel namespaces**: Container sees host PID tree, network, mounts. Not a sandbox.
- **ptrace overhead**: 3-8% for CLI tools; 20-50% for file-heavy ops; 10-100x for tight syscall loops.
- **SELinux variability**: Some OEM policies block `/proc/<pid>/comm` reads. Fallback to PID-only (documented <1% false-positive risk).
- **Scoped Storage (Android 10+)**: Cannot bind arbitrary `/sdcard` paths. Use `termux-setup-storage` and bind only granted subpaths.

## Phase 1 Scope Decisions
- **Blocking execution only**: `tbox run` does not support `-d`. Use shell `&` for backgrounding (exit codes not captured).
- **No port publishing**: Containers share host network. Bind services to `127.0.0.1`.
- **No orphan cleanup**: If proot is killed, traced processes may linger. Use `tbox ps` + manual `kill` if needed.
- **Tarball-based cache**: Repacking an image creates a new cache entry. Use `tbox image prune` (Phase 2) to manage disk.
- **No log rotation**: `stdout.log` grows unbounded. Monitor disk usage; Phase 2 adds size-based rotation.

## Unsupported Features (Fundamental)
- **No setuid support**: Binaries requiring setuid (`sudo`, `ping`) will fail. Use static alternatives.
- **No device node access**: `/dev/null`, `/dev/zero` emulated; hardware devices (`/dev/sda`) inaccessible.
- **No real isolation**: By design — tbox is for convenience, not security.
