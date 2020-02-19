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

// 比 "golang.org/x/crypto/ssh" 的优势：依靠 context 来做超时管理，随心所欲的结束任务
// 同类别的 package https://github.com/cosiner/socker 更加复杂
//    而且依旧没有能力使用 context 做即时退出

type Remoter struct {
	clt    *ssh.Client
	closer io.Closer // 记录 ssh 底层使用的 TCP 连接 为了有能力通过 context 退出
	Host   string
	User   string
	Port   string
}

func (r *Remoter) withContext(waitCtx context.Context) (chan bool, *sync.WaitGroup) {
	funcExit := make(chan bool, 1)
	waitGrp := new(sync.WaitGroup)
	waitGrp.Add(1)
	go func() {
		select {
		case <-funcExit:
		case <-waitCtx.Done():
			_ = r.closer.Close()
		}
		waitGrp.Done()
	}()
	return funcExit, waitGrp
}

// dialContext is copy from ssh.Dial(), with add context arg
func (r *Remoter) dialContext(waitCtx context.Context, network, addr string,
	config *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(waitCtx, network, addr)
	if err != nil {
		return nil, err
	}
	r.closer = conn

	noNeedWait, waitGrp := r.withContext(waitCtx)
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	close(noNeedWait)
	waitGrp.Wait()
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// Dial remote machine
// sshConf["host"] = "1.1.1.1"
// sshConf["auth"] = ssh.AuthMethod
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
// diff with Output() method, all command share one same stdout
func (r *Remoter) Run(waitCtx context.Context, cmds []string, stdout io.Writer, stderr io.Writer) error {

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
	noNeedWait, waitGrp := r.withContext(waitCtx)
	err = ssn.Wait()
	close(noNeedWait)
	waitGrp.Wait()
	return err
}

// Output run a command, and return stdout && stderr
// Also return the executed command, when cmd is compose by prefix,
// we need know cmd in caller
func (r *Remoter) Output(waitCtx context.Context, cmd string) (string, []byte, error) {
	// cannot use defer close(r.withContext()) , defer may delay called,
	// and cause a race condition
	clt := r.clt
	ssn, err := clt.NewSession()
	if err != nil {
		return cmd, nil, err
	}
	// stdout stderr all in one
	// BUG: there will have a race condition
	//   sub routine and this routine operate same r.closer
	noNeedWait, waitGrp := r.withContext(waitCtx)
	b, err := ssn.CombinedOutput(cmd)
	// tell sub routine to exit
	close(noNeedWait)
	// withContext sub routine exit
	waitGrp.Wait()
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
	noWait, waitGrp := r.withContext(waitCtx)
	_, err = io.Copy(wf, rf)
	close(noWait)
	waitGrp.Wait()
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
	noWait, waitGrp := r.withContext(waitCtx)
	_, err = io.Copy(wf, rf)
	close(noWait)
	waitGrp.Wait()
	_ = wf.Close()
	_ = rf.Close()
	_ = clt.Close()
	return err
}
