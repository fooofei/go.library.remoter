package goremoter

import (
    "context"
    "fmt"
    "io"
    "strings"
)

// PathPrefix is the command path prefix
// If `cd /home` is prefix, then ls -al . command will be
// `cd /home && ls -al .`
type pathPrefix struct {
    prefix  string
    remoter *Remoter
}

// WithPrefix return the PathPrefix
func WithPrefix(prefix string, remoter *Remoter) *pathPrefix {
    return &pathPrefix{
        prefix:  prefix,
        remoter: remoter,
    }
}

// Run with path prefix
func (p *pathPrefix) Run(waitCtx context.Context, cmds []string, stdout io.Writer, stderr io.Writer) error {
    cmdsWithPrefix := make([]string, 0)
    for _, cmd := range cmds {
        cmdsWithPrefix = append(cmdsWithPrefix,
            fmt.Sprintf("cd %s && %s", p.prefix, cmd))
    }
    return p.remoter.Run(waitCtx, cmdsWithPrefix, stdout, stderr)
}

// Output with path prefix
func (p *pathPrefix) Output(waitCtx context.Context, cmd string) (string, []byte, error) {
    return p.remoter.Output(waitCtx, fmt.Sprintf("cd %s && %s", p.prefix, cmd))
}

// Put with path prefix
func (p *pathPrefix) Put(waitCtx context.Context, local string, remote string) error {
    if !strings.HasPrefix(remote, "/") {
        // remote need to add prefix
        v := p.prefix
        if !strings.HasSuffix(v, "/") {
            v = fmt.Sprintf("%s/", v)
        }
        remote = fmt.Sprintf("%s%s", v, remote)
    }
    return p.remoter.Put(waitCtx, local, remote)
}

// Get with path prefix
func (p *pathPrefix) Get(waitCtx context.Context, remote string, local string) error {
    if !strings.HasPrefix(remote, "/") {
        // remote need to add prefix
        v := p.prefix
        if !strings.HasSuffix(v, "/") {
            v = fmt.Sprintf("%s/", v)
        }
        remote = fmt.Sprintf("%s%s", v, remote)
    }
    return p.remoter.Get(waitCtx, remote, local)
}
