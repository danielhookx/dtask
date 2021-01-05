package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime/pprof"
	"runtime/trace"
	"syscall"
	"time"

	gpprof "github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/oofpgDLD/dtask"
	"github.com/oofpgDLD/dtask/proto"
)

const (
	_trace     = false
	_pprof     = false
	_pprof_net = true
)

var addr = flag.String("addr", ":17070", "http service address")

var (
	tp *dtask.TaskPool
)

func main() {
	flag.Parse()
	if _trace {
		f, err := os.Create("trace.out")
		if err != nil {
			panic(err)
		}
		defer f.Close()

		err = trace.Start(f)
		if err != nil {
			panic(err)
		}
		defer trace.Stop()
	}

	if _pprof {
		f, err := os.Create("pprof.out")
		if err != nil {
			panic(err)
		}
		err = pprof.StartCPUProfile(f)
		if err != nil {
			panic(err)
		}
		defer pprof.StopCPUProfile()
	}

	r := gin.Default()
	// websocket
	r.GET("/test", ServeWs)
	r.GET("/test2", func(context *gin.Context) {
		context.String(http.StatusOK, "success")
	})

	srv := &http.Server{
		Addr:    *addr,
		Handler: r,
	}
	if _pprof_net {
		log.Printf("serve %s", *addr)
		gpprof.Register(r)
	}
	go func() {
		err := srv.ListenAndServe()
		if err != nil {
			log.Printf("http server stop %v", err)
		}
	}()
	tp = dtask.NewTaskPool(10)
	// init signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT)
	for {
		s := <-c
		log.Printf("service get a signal %s", s.String())
		switch s {
		case syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT:
			log.Print("server exit")
			return
		case syscall.SIGHUP:
			// TODO reload
		default:
			return
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,

	// allow cross-origin
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

func ServeWs(context *gin.Context) {
	var (
		userId string
		tk     *dtask.Task
		wCh    chan []byte
	)
	userId = context.GetHeader("FZM-UID")
	c, err := upgrader.Upgrade(context.Writer, context.Request, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	tk = tp.Add(time.Second * 2)
	wCh = make(chan []byte)
	defer c.Close()
	defer onClose(userId, tk)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				return
			}
			log.Printf("recv: %s", message)
			onRecvMsg(tk, message)
		}
	}()

	for {
		select {
		case <-done:
			return
		case data := <-wCh:
			err = c.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				log.Println("write:", err)
				break
			}
		}
	}
}

func onRecvMsg(tk *dtask.Task, msg []byte) {
	msg = bytes.TrimSpace(bytes.Replace(msg, newline, space, -1))
	p := proto.Proto{}
	json.Unmarshal(msg, &p)
	switch p.Opt {
	case proto.Start:
		tk.Add(string(p.Seq), func(i int64) func() {
			return func() {
				time.Sleep(1 * time.Second)
				//fmt.Printf("i am:%v\n", i)
				//tk.Done(string(i))
			}
		}(p.Seq))
	case proto.Stop:
		tk.Done(string(p.Seq))
	default:
	}
}

func onClose(userId string, tk *dtask.Task) {
	if userId == "" || tk == nil {
		log.Printf("onClose WsConnection args can not find")
		return
	}
	tp.Del(tk)
}
