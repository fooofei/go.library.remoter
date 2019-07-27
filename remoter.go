package goremoter

import (
    "context"
    "fmt"
    "github.com/pkg/sftp"
    "golang.org/x/crypto/ssh"
    "io"
    "io/ioutil"
    "net"
    "os"
)

// 比 "golang.org/x/crypto/ssh" 的优势：依靠context 来做超时管理，随心所欲的结束任务

type Remoter struct {
    clt    *ssh.Client
    closer io.Closer // 记录 ssh 底层使用的 TCP 连接
    // 为了有能力通过 context 退出
    Host string
    User string
    Port string
}

func (r *Remoter) wait(waitCtx context.Context) chan bool {
    funcExit := make(chan bool, 1)
    go func() {
        select {
        case <-funcExit:
        case <-waitCtx.Done():
            _ = r.closer.Close()
        }
    }()
    return funcExit
}

// Copyed from ssh.Dial(), add context
func (r *Remoter) dialContext(waitCtx context.Context, network, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
    d := net.Dialer{}
    conn, err := d.DialContext(waitCtx, network, addr)
    if err != nil {
        return nil, err
    }
    r.closer = conn
    //
    defer close(r.wait(waitCtx))
    c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
    if err != nil {
        return nil, err
    }
    return ssh.NewClient(c, chans, reqs), nil
}

// Dial remote machine
// sshConf["host"] = "1.1.1.1"
// optional sshConf["password"]=""
// sshConf["privKey"] = <fullPath>
// sshConf["port"] =
// sshConf["user"] =
func Dial(waitCtx context.Context, sshConf map[string]interface{}) (*Remoter, error) {
    r := &Remoter{}
    var exists bool
    r.Host, exists = sshConf["host"].(string)
    if !exists {
        return nil, fmt.Errorf("conf not exists host")
    }
    r.User, exists = sshConf["user"].(string)
    if !exists {
        return nil, fmt.Errorf("conf not exists user")
    }
    r.Port, exists = sshConf["port"].(string)
    if !exists {
        return nil, fmt.Errorf("conf not exists port")
    }
    addr := fmt.Sprintf("%v:%v", r.Host, r.Port)

    auth := make([]ssh.AuthMethod, 0)
    if pass, ok := sshConf["password"].(string); ok {
        auth = append(auth, ssh.Password(pass))
    } else {
        privKeyFileName := sshConf["privKey"].(string)
        privKeyBytes, _ := ioutil.ReadFile(privKeyFileName)
        privKey, err := ssh.ParsePrivateKey(privKeyBytes)
        if err != nil {
            return nil, err
        }
        auth = append(auth, ssh.PublicKeys(privKey))
    }

    hostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
        return nil
    }

    cltConf := ssh.ClientConfig{
        User:            r.User,
        Auth:            auth,
        HostKeyCallback: hostKeyCallback,
    }
    // 这里有 TCP 三次握手超时和 SSH 握手超时
    clt, err := r.dialContext(waitCtx, "tcp", addr, &cltConf)
    if err != nil {
        return nil, err
    }
    r.clt = clt
    return r, nil
}

// Close the ssn client
func (r *Remoter) Close() error {
    r.closer = nil
    if r.clt != nil {
        return r.clt.Close()
    }
    return nil
}

// Run commands
// command not terminate by '\n' ,
// commands not include "exit"
// If a middle command fails, the next command will run ignore error
func (r *Remoter) Run(waitCtx context.Context, cmds []string, stdout io.Writer, stderr io.Writer) error {
    defer close(r.wait(waitCtx))

    clt := r.clt
    ssn, err := clt.NewSession()
    if err != nil {
        return err
    }
    defer ssn.Close()

    // not know why use this
    // keep it for someday I know this
    //modes := ssh.TerminalModes{
    //    ssh.ECHO:          0,     // disable echoing
    //    ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
    //    ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
    //}
    //_ = modes
    //if err := ssn.RequestPty("xterm", 80, 40, modes); err != nil {
    //    return nil
    //}

    shellInput, err := ssn.StdinPipe()
    if err != nil {
        return err
    }
    ssn.Stdout = stdout
    ssn.Stderr = stderr
    err = ssn.Shell()
    if err != nil {
        return err
    }

    for _, cmd := range cmds {
        _, err = fmt.Fprintf(shellInput, "%s\n", cmd)
        if err != nil {
            return err
        }
    }
    // we must have exit to return `Wait()`
    _, err = fmt.Fprintf(shellInput, "exit\n")
    if err != nil {
        return err
    }
    // all command after exit will not run
    err = ssn.Wait()
    return err
}

// Output run a command, and return stdout && stderr
// Also return the executed command, when cmd is compose by prefix,
// we need know cmd in caller
func (r *Remoter) Output(waitCtx context.Context, cmd string) (string, []byte, error) {
    defer close(r.wait(waitCtx))

    clt := r.clt
    ssn, err := clt.NewSession()
    if err != nil {
        return cmd, nil, err
    }
    // stdout stderr all in one
    b, err := ssn.CombinedOutput(cmd)
    _ = ssn.Close()
    return cmd, b, err
}

// Put local file to remote, remove remote first in case of exists
func (r *Remoter) Put(waitCtx context.Context, local string, remote string) error {
    clt, err := sftp.NewClient(r.clt)
    if err != nil {
        return err
    }
    // ignore error remove first
    _ = clt.Remove(remote)
    rf, err := os.Open(local)
    if err != nil {
        _ = clt.Close()
        return err
    }
    wf, err := clt.Create(remote)

    if err != nil {
        _ = clt.Close()
        _ = rf.Close()
        return err
    }
    _, err = io.Copy(wf, rf)
    _ = rf.Close()
    _ = wf.Close()
    _ = clt.Close()
    return err
}

// Get remote file to local, remove local file first in case of exists
func (r *Remoter) Get(waitCtx context.Context, remote string, local string) error {
    clt, err := sftp.NewClient(r.clt)
    if err != nil {
        return err
    }

    _ = os.Remove(local)
    rf, err := clt.Open(remote)
    if err != nil {
        _ = clt.Close()
        return err
    }
    wf, err := os.Create(local)
    if err != nil {
        _ = clt.Close()
        _ = rf.Close()
        return err
    }
    _, err = io.Copy(wf, rf)
    _ = wf.Close()
    _ = rf.Close()
    _ = clt.Close()
    return err
}
