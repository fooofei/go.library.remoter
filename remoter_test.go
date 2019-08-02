package goremoter

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "time"
    "testing"
)

// 例子：在主机上执行命令的集合，或者还可以有文件的传输
func singleRun(waitRootCtx context.Context, clt *Remoter,
    cmdsTimeout time.Duration,
    results chan string) {

    cmds := []string{
        ("echo hello"),
    }
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

    bb := bytes.NewBufferString("")
    for _, cmd := range cmds {
        bb.WriteString(fmt.Sprintf("--> ssh %s@%s -p %s exec %s\n",
            clt.User, clt.Host, clt.Port,
            cmd))
        cr := clt.Output(waitCtx, cmd)
        if cr.Err != nil {
            bb.WriteString(fmt.Sprintf("err=%v\n", cr.Err))
        } else {
            bb.WriteString(fmt.Sprintf("%s\n", cr.Out))
        }
    }
    close(noNeedWait)
    results <- bb.String()
}

// 在单个主机上运行
// waitRootCtx 控制强制退出
// sshConf 主机连接需要的信息
// results 放命令运行结果的队列
// online 连接成功后说明在线，放到这里
// offline 连接失败后说明离线，放到这里
func single(waitRootCtx context.Context, sshConf map[string]interface{}, results chan string,
    online chan map[string]interface{}, offline chan map[string]interface{}) {
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
        results <- fmt.Sprintf("fail dial %v err=%v\n",
            sshConf, err)
        offline <- sshConf
        return
    }

    online <- sshConf

    singleRun(waitRootCtx, clt, cmdsTimeout, results)
    _ = clt.Close()
}

func jsonDumpsMap(m interface{}) string {
    b, _ := json.Marshal(m)
    return string(b)
}


func TestDeploy(t *testing.T){
    vms := map[string]interface{}{
        "user":  "root",
        "host":  "1.1.1.1", // put your host here when test
        "port":  "22",
        "label": "China",
    }

    vms["connectTimeout"] = time.Second * 500
    vms["connectTryTimes"] = 4
    vms["cmdsTimeout"] = time.Second * 200
    homePath,err := os.UserHomeDir()
    if err != nil {
        panic(err)
    }
    vms["privKey"] = filepath.Join(homePath, ".ssh/id_ed25519") // put ssh-key or password

    fmt.Printf("vms= %v\n", jsonDumpsMap(vms))
    results := make(chan string, len(vms)*2)
    online := make(chan map[string]interface{}, len(vms)*2)
    offline := make(chan map[string]interface{}, len(vms)*2)
    waitCtx, cancel := context.WithCancel(context.Background())
    single(waitCtx, vms, results, online, offline)

    close(results)
    close(online)
    close(offline)
    fmt.Printf("results=\n")
    for result := range results {
        fmt.Printf("    %v\n", result)
    }
    fmt.Printf("online=\n")
    for on := range online {
        fmt.Printf("    %v\n", jsonDumpsMap(on))
    }

    fmt.Printf("offline=\n")
    for off := range offline {
        fmt.Printf("    %v\n", jsonDumpsMap(off))
    }

    cancel()
    fmt.Printf("main exit")
}
