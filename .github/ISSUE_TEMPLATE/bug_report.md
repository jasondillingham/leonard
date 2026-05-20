---
name: Bug report
about: Something Leonard did that surprised you
title: ''
labels: bug
assignees: ''
---

## Summary

<!-- 1-2 sentences. What did Leonard do that you didn't expect? -->

## Reproducer

<!--
The most useful filing shape is a minimal reproducer:
  1. fresh leonard install (paste `leonard --version`)
  2. exact commands
  3. observed output
  4. expected output

Even a partial reproducer is more useful than a description. The
bug-hunt discipline (see audits/) thrives on concrete repros.
-->

```
$ leonard --version
$ # commands that surface the issue
```

## Environment

- OS: <!-- macOS 14.5, Ubuntu 22.04, etc. -->
- Go version: <!-- `go version` -->
- Leonard version: <!-- `leonard --version` -->
- Rust toolchain (if relevant): <!-- `cargo --version` -->

## Context

<!--
What were you trying to accomplish? If this surfaces in a specific
project shape (monorepo, polyglot, large dep tree), call that out
so we can frame the reproducer.
-->
