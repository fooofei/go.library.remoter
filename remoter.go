// 比 "golang.org/x/crypto/ssh" 的优势：依靠 context 来做超时管理，随心所欲的结束任务
// 同类别的 package https://github.com/cosiner/socker 更加复杂
//    而且依旧没有能力使用 context 做即时退出

// 20200419
// 尝试通过 SetReadDeadline 技巧做超时管理，发现 ssh 库不支持这种技巧
// 当 SetReadDeadline 之后，ssh 库就会关闭 底层 TCP 连接
// 代码细节 在这里
// ssh\handshake.go
//func (t *handshakeTransport) readLoop() {
//	got err poll.TimeoutErr i/o timeout
//	然后退出 readLoop()
//接着退出 (t *handshakeTransport) kexLoop()
//然后调用底层 TCP 连接 Close()，发送 RST

package goremoter

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type dialConfig struct {
	Network   string
	Addr      string
	CltConfig *ssh.ClientConfig
}

type Remoter struct {
	clt          *ssh.Client
	closer       io.Closer // 记录 ssh 底层使用的 TCP 连接 为了有能力通过 context 退出
	backupConfig dialConfig
	Host         string
	User         string
	Port         string
}

func enterContext(waitCtx context.Context, closer io.Closer) (chan bool, *sync.WaitGroup) {
	funcExit := make(chan bool, 1)
	waitGrp := new(sync.WaitGroup)
	waitGrp.Add(1)
	go func() {
		select {
		case <-funcExit:
		case <-waitCtx.Done():
			_ = closer.Close()
		}
		waitGrp.Done()
	}()
	return funcExit, waitGrp
}

func leaveContext(noNeedWait chan bool, waitGrp *sync.WaitGroup) {
	close(noNeedWait)
	waitGrp.Wait()
}

func (r *Remoter) dial(waitCtx context.Context) error {
	conn, clt, err := dialContext(waitCtx, r.backupConfig.Network,
		r.backupConfig.Addr, r.backupConfig.CltConfig)
	if err != nil {
		return err
	}
	r.closer = conn
	r.clt = clt
	return nil
}

func (r *Remoter) makeSureConnecting(waitCtx context.Context) error {
	if r.clt == nil {
		return r.dial(waitCtx)
	}
	return nil
}

// Dial remote machine
// sshConf["host"] = "1.1.1.1"
// sshConf["auth"] = []ssh.AuthMethod
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
	addr := net.JoinHostPort(r.Host, r.Port)

	var auth []ssh.AuthMethod
	auth, exists = sshConf["auth"].([]ssh.AuthMethod)
	if !exists {
		return nil, fmt.Errorf("conf not exists auth")
	}
	hostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return nil
	}

	cltConf := &ssh.ClientConfig{
		User:            r.User,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
	}
	r.backupConfig.Addr = addr
	r.backupConfig.Network = "tcp"
	r.backupConfig.CltConfig = cltConf
	// 这里有 TCP 三次握手超时和 SSH 握手超时
	err := r.dial(waitCtx)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Close the ssn client
func (r *Remoter) Close() error {
	r.closer = nil
	if r.clt != nil {
		// will close r.closer
		err := r.clt.Close()
		r.clt = nil
		return err
	}
	return nil
}

// Run commands
// command not terminate by '\n' ,
// commands not include "exit"
// If a middle command fails, the next command will run ignore error
// diff with Output() method, all command share one same stdout
func (r *Remoter) Run(waitCtx context.Context, cmds []string, stdout io.Writer, stderr io.Writer) error {
	err := r.makeSureConnecting(waitCtx)
	if err != nil {
		return err
	}
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
	arg1, arg2 := enterContext(waitCtx, r)
	err = ssn.Wait()
	leaveContext(arg1, arg2)
	return err
}

// Output run a command, and return stdout && stderr
// Also return the executed command, when cmd is compose by prefix,
// we need know cmd in caller
func (r *Remoter) Output(waitCtx context.Context, cmd string) (string, []byte, error) {
	err := r.makeSureConnecting(waitCtx)
	if err != nil {
		return cmd, nil, err
	}
	// cannot use defer close(enterContext()) , defer may delay called,
	// and cause a race condition
	clt := r.clt
	ssn, err := clt.NewSession()
	if err != nil {
		return cmd, nil, err
	}
	// stdout stderr all in one
	arg1, arg2 := enterContext(waitCtx, r)
	b, err := ssn.CombinedOutput(cmd)
	leaveContext(arg1, arg2)
	_ = ssn.Close()
	return cmd, b, err
}

// Put local file to remote, remove remote first in case of exists
func (r *Remoter) Put(waitCtx context.Context, local string, remote string) error {
	err := r.makeSureConnecting(waitCtx)
	if err != nil {
		return err
	}
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
	arg1, arg2 := enterContext(waitCtx, r)
	_, err = io.Copy(wf, rf)
	leaveContext(arg1, arg2)
	_ = rf.Close()
	_ = wf.Close()
	_ = clt.Close()
	return err
}

// Get remote file to local, remove local file first in case of exists
func (r *Remoter) Get(waitCtx context.Context, remote string, local string) error {
	err := r.makeSureConnecting(waitCtx)
	if err != nil {
		return err
	}
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
	arg1, arg2 := enterContext(waitCtx, r)
	_, err = io.Copy(wf, rf)
	leaveContext(arg1, arg2)
	_ = wf.Close()
	_ = rf.Close()
	_ = clt.Close()
	return err
}

// dialContext is copy from ssh.Dial(), with add context arg
func dialContext(waitCtx context.Context, network, addr string,
	config *ssh.ClientConfig) (net.Conn, *ssh.Client, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(waitCtx, network, addr)
	if err != nil {
		return nil, nil, err
	}
	arg1, arg2 := enterContext(waitCtx, conn)
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	leaveContext(arg1, arg2)
	if err != nil {
		return nil, nil, err
	}
	return conn, ssh.NewClient(c, chans, reqs), nil
}
