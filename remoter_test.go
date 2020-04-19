package goremoter

import (
	"context"
	"io/ioutil"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gotest.tools/assert"
)

func canRunOtherCommandsAfterTimeout(waitRootCtx context.Context, clt *Remoter, t *testing.T) {
	for i := 0; i < 5; i++ {
		waitCtx, _ := context.WithTimeout(waitRootCtx, 2*time.Second)
		t.Logf("enter %v", time.Now().Format(time.RFC3339))
		_, _, err := clt.Output(waitCtx, "sleep 4")
		t.Logf("leave %v err= %v", time.Now().Format(time.RFC3339), err)
		_, out, err := clt.Output(waitRootCtx, "echo other")
		if err != nil {
			t.Logf("cannot execute command after timeout err= %v", err)
		} else {
			assert.Equal(t, string(out), "other\n")
		}
	}
}

// 例子：在主机上执行命令的集合，或者还可以有文件的传输
func runCommandsOnRemoter(waitRootCtx context.Context, clt *Remoter,
	cmdsTimeout time.Duration, t *testing.T) {
	waitCtx, _ := context.WithTimeout(waitRootCtx, cmdsTimeout)
	_, out, err := clt.Output(waitCtx, "echo hello")
	if err != nil {
		t.Logf("err= %v", err)
	} else {
		assert.Equal(t, string(out), "hello\n")
	}
	canRunOtherCommandsAfterTimeout(waitRootCtx, clt, t)
	start := time.Now()
	err = clt.Put(waitCtx, "/bigfile",
		"/root/bigfile")
	t.Logf("put err= %v take= %v(s)", err, int64(time.Since(start).Seconds()))
}

// 在单个主机上运行
// waitRootCtx 控制强制退出
// sshConf 主机连接需要的信息
// results 放命令运行结果的队列
// online 连接成功后说明在线，放到这里
// offline 连接失败后说明离线，放到这里
func runCommandsOnHost(waitRootCtx context.Context, sshConf map[string]interface{}, t *testing.T) {
	// 把在一个主机上运行分为 2 个部分，
	// 第 1 部分是 Dial， 连接主机，分几次重试，每次独立的超时
	// 第 2 部分是命令执行，所有命令执行总时间设置超时
	connectTimeout := sshConf["connectTimeout"].(time.Duration)
	connectTryTimes := sshConf["connectTryTimes"].(int)
	cmdsTimeout := sshConf["cmdsTimeout"].(time.Duration)

	var clt *Remoter
	var err error
	for i := 0; i < connectTryTimes; i++ {
		waitCtx, _ := context.WithTimeout(waitRootCtx, connectTimeout)
		clt, err = Dial(waitCtx, sshConf)
		if err == nil {
			break
		}
	}

	if err != nil {
		t.Errorf("fail dial %v err=%v\n", sshConf, err)
		return
	}
	runCommandsOnRemoter(waitRootCtx, clt, cmdsTimeout, t)
	_ = clt.Close()
}

// withContext CTRL+C to force quit
func setupSignal(waitCtx context.Context, cancel context.CancelFunc) {

	sigCh := make(chan os.Signal, 2)

	signal.Notify(sigCh, os.Interrupt)
	signal.Notify(sigCh, syscall.SIGTERM)

	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-waitCtx.Done():
		}
	}()
}

func TestDeploy(t *testing.T) {
	vms := map[string]interface{}{
		"user":  "root",
		"host":  "1.1.1.1", // put your host here when test
		"port":  "22",
		"label": "China",
	}
	t.Logf("pid= %v\n", os.Getpid())
	vms["connectTimeout"] = time.Second * 3
	vms["connectTryTimes"] = 4
	vms["cmdsTimeout"] = time.Second * 5
	homePath, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(homePath, ".ssh/id_ed25519") // put ssh-key or password
	keyPEMBytes, err := ioutil.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	privKey, err := ssh.ParsePrivateKey(keyPEMBytes)
	if err != nil {
		t.Fatal(err)
	}
	vms["auth"] = []ssh.AuthMethod{ssh.PublicKeys(privKey)}

	waitCtx, cancel := context.WithCancel(context.Background())
	setupSignal(waitCtx, cancel)
	runCommandsOnHost(waitCtx, vms, t)

	cancel()
}
