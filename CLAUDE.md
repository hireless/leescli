# Lee CLI 개발 노트

## Termux에서 claude 실행 문제

Termux 환경에서 `exec.Command("claude", ...)` 직접 실행 시 에러 발생:

```
fork/exec /data/data/com.termux/files/usr/bin/claude: no such file or directory
```


### 원인
- claude는 symlink → `../lib/node_modules/@anthropic-ai/claude-code/cli.js`
- cli.js의 shebang: `#!/usr/bin/env node`
- Termux에서 `/usr/bin/env`가 없어서 직접 실행 불가

### 해결
`sh -c`를 통해 실행:

```go
// 안됨
cmd := exec.Command("claude", "-p", prompt)

// 됨
cmd := exec.Command("sh", "-c", "claude -p "+escaped)
```

## 빌드

빌드 파일은 **leescli** 하나만 유지한다. 다른 빌드 파일(lee, leecli, lees-cli 등)은 삭제할 것.

```bash
go build -o leescli main.go
```
