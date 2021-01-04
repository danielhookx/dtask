package main

import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"syscall"
	"time"

	"github.com/oofpgDLD/dtask"
)

const(
	_trace = false
	_pprof = false
	_pprof_net = true
)

var (
	times = 1000
	users = 100000
	port = "6060"
)

func main() {
	if len(os.Args) < 4{
		panic("error args")
	}
	us, _ := strconv.ParseInt(os.Args[1], 10, 64)
	ts, _ := strconv.ParseInt(os.Args[2], 10, 64)
	port = os.Args[3]
	if us == 0 {
		panic("err users number")
	}
	if ts == 0 {
		panic("err times number")
	}
	if port == "" {
		panic("err port number")
	}
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

	if _pprof_net{
		go http.ListenAndServe(fmt.Sprintf(":%v", port), nil)
	}

	tp := dtask.NewTaskPool(10)
	go func() {
		start := time.Now()
		for j:=0 ; j < users; j++ {
			go func() {
				for i := 0; i < times; i++ {
					//time.Sleep(500 * time.Millisecond)
					tk := tp.Add(time.Second * 2, func(i int) func() {
						return func() {
							time.Sleep(1 * time.Second)
							//fmt.Printf("i am:%v\n", i)
							//tk.Done(string(i))
						}
					}(i))
					go func(tk *dtask.Task) {
						time.Sleep(time.Second * 5)
						tp.Del(tk)
					}(tk)
				}
			}()
		}
		cost := time.Since(start)
		fmt.Printf("cost=[%s]",cost)
	}()
	// init signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT)
	for {
		s := <-c
		log.Print("service get a signal %s", s.String())
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
