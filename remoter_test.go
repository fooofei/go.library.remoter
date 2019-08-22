package goremoter

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"gotest.tools/assert"
)

// 例子：在主机上执行命令的集合，或者还可以有文件的传输
func singleRun(waitRootCtx context.Context, clt *Remoter,
	cmdsTimeout time.Duration, t *testing.T) {

	waitCtx, cancel := context.WithCancel(waitRootCtx)
	noNeedWait := make(chan bool)
	go func() {
		select {
		case <-waitCtx.Done():
		case <-time.After(cmdsTimeout):
			cancel()
		case <-noNeedWait:
		}
	}()

	_, out, err := clt.Output(waitCtx, "echo hello")
	if err != nil {
		t.Logf("err= %v", err)
	} else {
		assert.Equal(t, string(out), "hello\n")
	}

	start := time.Now()
	err = clt.Put(waitCtx, "/bigfile",
		"/root/bigfile")
	t.Logf("put err= %v take= %v(s)", err, int64(time.Since(start).Seconds()))

	close(noNeedWait)
}

// 在单个主机上运行
// waitRootCtx 控制强制退出
// sshConf 主机连接需要的信息
// results 放命令运行结果的队列
// online 连接成功后说明在线，放到这里
// offline 连接失败后说明离线，放到这里
func single(waitRootCtx context.Context, sshConf map[string]interface{}, t *testing.T) {
	//把在一个主机上运行分为 2 个部分，
	// 第 1 部分是 Dial， 连接主机，分几次重试，每次独立的超时
	// 第 2 部分是命令执行，所有命令执行总时间设置超时
	connectTimeout := sshConf["connectTimeout"].(time.Duration)
	connectTryTimes := sshConf["connectTryTimes"].(int)
	cmdsTimeout := sshConf["cmdsTimeout"].(time.Duration)

	var clt *Remoter
	var err error
	for i := 0; i < connectTryTimes; i++ {
		waitCtx, cancel := context.WithCancel(waitRootCtx)
		noNeedWait := make(chan bool)
		go func() {
			select {
			case <-waitCtx.Done():
			case <-time.After(connectTimeout):
				cancel()
			case <-noNeedWait:
			}
		}()
		clt, err = Dial(waitCtx, sshConf)
		close(noNeedWait)
		if err == nil {
			break
		}
	}

	if err != nil {
		t.Errorf("fail dial %v err=%v\n",
			sshConf, err)
		return
	}
	singleRun(waitRootCtx, clt, cmdsTimeout, t)
	_ = clt.Close()
}

func jsonDumpsMap(m interface{}) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// wait CTRL+C to force quit
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
	vms["connectTimeout"] = time.Second * 500
	vms["connectTryTimes"] = 4
	vms["cmdsTimeout"] = time.Second * 5
	homePath, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	// or vms["password"]= "xxx"
	vms["privKey"] = filepath.Join(homePath, ".ssh/id_ed25519") // put ssh-key or password

	waitCtx, cancel := context.WithCancel(context.Background())
	setupSignal(waitCtx, cancel)
	single(waitCtx, vms, t)

	cancel()
}
