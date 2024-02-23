package graceful

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/spf13/cast"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var originalWD, _ = os.Getwd()

const (
	Default    = "default"
	Docker     = "docker"
	ginServer  = "ginServer"
	fxApp      = "fxApp"
	taskServer = "taskServer"
)

func RunGinServer(httpServer *http.Server, opt *Options) error {
	s := newServer(opt, ginServer)
	s.httpServer = httpServer
	return s.run()
}
func RunFxApp(app FxApp, opt *Options) error {
	s := newServer(opt, fxApp)
	s.fxApp = app
	return s.run()
}
func RunTaskServer(server TaskServer, opt *Options) error {
	s := newServer(opt, taskServer)
	s.taskServer = server
	return s.run()
}

type FxApp interface {
	Stop(ctx context.Context) (err error)
	Start(ctx context.Context) (err error)
}

type TaskServer interface {
	Start()
	Stop() context.Context
}
type Options struct {
	RunMode             string
	StopTimeoutDuration time.Duration
}
type server struct {
	opt        *Options
	serverType string
	httpServer *http.Server
	fxApp      FxApp
	taskServer TaskServer
}

func (s *server) initOption() {
	if s.opt == nil {
		s.opt = &Options{
			RunMode: Default,
		}
	}
	if s.opt.RunMode == "" {
		s.opt.RunMode = Docker
	}
	if s.opt.StopTimeoutDuration == 0 {
		s.opt.StopTimeoutDuration = time.Second * time.Duration(15)
	}
}
func newServer(opt *Options, serverType string) *server {
	s := &server{
		opt:        opt,
		serverType: serverType,
	}
	s.initOption()
	return s
}
func (s *server) run() error {
	go func() {
		if s.serverType == ginServer {
			if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				panic(err)
			}
		} else if s.serverType == taskServer {
			s.taskServer.Start()
		} else {
			err := s.fxApp.Start(context.Background())
			if err != nil {
				panic(err)
			}
		}
		if s.opt.RunMode == Default {
			_ = s.writeRestartScript(os.Getpid())
		}
	}()
	// 等待中断信号来优雅地关闭服务器
	ch := make(chan os.Signal, 10)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR2)
	for {
		select {
		case sig := <-ch:
			switch sig {
			case syscall.SIGINT, syscall.SIGTERM:
				return s.stop(false)
			case syscall.SIGUSR2:
				return s.stop(true)
			}
			return nil
		default:
		}
	}
}
func (s *server) stop(isRestart bool) error {
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(15))
	defer cancel()
	var err error
	switch s.serverType {
	case ginServer:
		err = s.httpServer.Shutdown(stopCtx)
	case fxApp:
		err = s.fxApp.Stop(stopCtx)
	case taskServer:
		rCtx := s.taskServer.Stop()
		for {
			select {
			case <-rCtx.Done():
				break
			}
		}
	}
	if err != nil {
		return err
	}
	if isRestart {
		return s.restartServer()
	}
	return nil
}
func (s *server) writeRestartScript(pid int) error {
	return writeRestartScript(pid)
}
func (s *server) restartServer() error {
	argv0, err := exec.LookPath(os.Args[0])
	if err != nil {
		//fmt.Println("start new process err:" + err.Error())
		return errors.New(fmt.Sprintf("start new process err:%s", err.Error()))
	} else {
		process, err := os.StartProcess(argv0, os.Args, &os.ProcAttr{
			Dir:   originalWD,
			Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
			// 新进程的环境变量
			Env: os.Environ(),
		})
		if err != nil {
			//fmt.Println("start new process err:" + err.Error())
			return errors.New(fmt.Sprintf("start new process err:%s", err.Error()))
		} else {
			//fmt.Println("start new process success, new pid:", process.Pid)
			return s.writeRestartScript(process.Pid)
		}
	}
}

var scriptTemplate = `#!/bin/bash
kill -USR2 27638
`

func writeRestartScript(pid int) error {
	filePath := "restart.sh"
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE, 0666)
	if err != nil {
		//fmt.Println("文件打开失败", err)
		return errors.New("文件打开失败:" + err.Error())
	}
	defer func() {
		_ = file.Close()
	}()
	write := bufio.NewWriter(file)
	_, err = write.WriteString(strings.Replace(scriptTemplate, "27638", cast.ToString(pid), -1))
	if err != nil {
		return errors.New("写入脚本失败:" + err.Error())
	}
	err = write.Flush()
	if err != nil {
		return errors.New("写入脚本失败:" + err.Error())
	}
	exec.Command("chmod", "+x", "restart.sh")
	return nil
}
