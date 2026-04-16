# tbox Security Model

> ⚠️ **tbox is NOT a security boundary**.
> Container processes run as the Termux app UID and share the host PID tree, 
> network stack, and mount table. Any host path you explicitly bind is fully 
> accessible to the container. **Do not run untrusted code**.

## Safe Usage ✅
- Run your own trusted CLI tools (curl, python, node, static binaries)
- Bind only non-sensitive host directories (`~/projects`, not `/data/data`)
- Use for prototyping, learning, and lightweight dev/ops tasks

## Unsafe Usage ❌
- Run untrusted or third-party binaries from the internet
- Bind sensitive paths: `/data/data`, `/system`, `/sdcard/Android`
- Assume network or process isolation (container shares host stack)

## Zip Slip Protection
tbox validates every path during rootfs extraction. Archives containing 
`../` path traversal or absolute symlinks pointing outside the container 
are rejected with an error.

## Vulnerability Reports
Report security issues to security@tbox.run with:
- Android version + API level
- Termux package versions (`pkg list-installed`)
- Steps to reproduce the issue
