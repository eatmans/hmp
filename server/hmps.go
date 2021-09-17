package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/natefinch/lumberjack"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

const  hmpLogo = `__ __  ___ ___  ____  
|  |  ||   |   ||    \ 
|  |  || _   _ ||  o  )
|  _  ||  \_/  ||   _/ 
|  |  ||   |   ||  |   
|  |  ||   |   ||  |   
|__|__||___|___||__|`

var (
	localPort int
	remotePort int
)
var sugarLogger *zap.SugaredLogger

func init()  {
	flag.IntVar(&localPort, "l", 25200,"本地端端口")
	flag.IntVar(&remotePort, "r", 25300,"客户端端口")
}

func InitLogger() {
	writeSyncer := getLogWriter()
	encoder := getEncoder()
	var writes = []zapcore.WriteSyncer{writeSyncer}
	// 如果是开发环境，同时在控制台上也输出
	debug := true
	if debug {
		writes = append(writes, zapcore.AddSync(os.Stdout))
	}
	//core := zapcore.NewCore(encoder, writeSyncer, zapcore.DebugLevel)
	core := zapcore.NewCore(encoder, zapcore.NewMultiWriteSyncer(writes...), zapcore.DebugLevel)

	logger := zap.New(core, zap.AddCaller())
	sugarLogger = logger.Sugar()
}

func getEncoder() zapcore.Encoder {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	return zapcore.NewConsoleEncoder(encoderConfig)
}

func getLogWriter() zapcore.WriteSyncer {
	lumberJackLogger := &lumberjack.Logger{
		Filename:   "./test.log",
		MaxSize:    1,
		MaxBackups: 5,
		MaxAge:     30,
		Compress:   false,
	}
	return zapcore.AddSync(lumberJackLogger)
}

type client struct {
	conn net.Conn
	// 数据传输通道
	read  chan []byte
	write chan []byte
	// 异常退出通道
	exit chan error
	// 重连通道
	reConn chan bool
}

// 从Client端读取数据
func (c *client) Read(ctx context.Context) {
	// 如果10秒钟内没有消息传输，则Read函数会返回一个timeout的错误
	_ = c.conn.SetReadDeadline(time.Now().Add(time.Second * 10))
	for {
		select {
		case <-ctx.Done():
			return
		default:
			data := make([]byte, 10240)
			n, err := c.conn.Read(data)
			if err != nil && err != io.EOF {
				if strings.Contains(err.Error(), "timeout") {
					// 设置读取时间为3秒，3秒后若读取不到, 则err会抛出timeout,然后发送心跳
					_ = c.conn.SetReadDeadline(time.Now().Add(time.Second * 3))
					c.conn.Write([]byte("pi"))
					continue
				}
				sugarLogger.Error("读取出现错误...")
				//fmt.Println("读取出现错误...")
				c.exit <- err
				return
			}

			// 收到心跳包,则跳过
			if data[0] == 'p' && data[1] == 'i' {
				sugarLogger.Info("server收到心跳包")
				//fmt.Println("server收到心跳包")
				continue
			}
			c.read <- data[:n]
		}
	}
}

// 将数据写入到Client端
func (c *client) Write(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-c.write:
			_, err := c.conn.Write(data)
			if err != nil && err != io.EOF {
				c.exit <- err
				return
			}
		}
	}
}

type user struct {
	conn net.Conn
	// 数据传输通道
	read  chan []byte
	write chan []byte
	// 异常退出通道
	exit chan error
}

// 从User端读取数据
func (u *user) Read(ctx context.Context) {
	_ = u.conn.SetReadDeadline(time.Now().Add(time.Second * 200))
	for {
		select {
		case <-ctx.Done():
			return
		default:
			data := make([]byte, 10240)
			n, err := u.conn.Read(data)
			if err != nil && err != io.EOF {
				u.exit <- err
				return
			}
			u.read <- data[:n]
		}
	}
}

// 将数据写给User端
func (u *user) Write(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-u.write:
			_, err := u.conn.Write(data)
			if err != nil && err != io.EOF {
				u.exit <- err
				return
			}
		}
	}
}

func main() {
	flag.Parse()

	InitLogger()

	defer func() {
		err := recover()
		if err != nil {
			fmt.Println(err)
		}
	}()

	clientListener, err := net.Listen("tcp", fmt.Sprintf(":%d", remotePort))
	if err != nil {
		panic(err)
	}
	sugarLogger.Infof("监听:%d端口, 等待client连接... \n", remotePort)
	//fmt.Printf("监听:%d端口, 等待client连接... \n", remotePort)
	// 监听User来连接
	userListener, err := net.Listen("tcp", fmt.Sprintf(":%d", localPort))
	if err != nil {
		panic(err)
	}
	sugarLogger.Infof("监听:%d端口, 等待user连接.... \n", localPort)
	//fmt.Printf("监听:%d端口, 等待user连接.... \n", localPort)

	for {
		// 有Client来连接了
		clientConn, err := clientListener.Accept()
		if err != nil {
			panic(err)
		}
		sugarLogger.Infof("有Client连接: %s \n", clientConn.RemoteAddr())
		//fmt.Printf("有Client连接: %s \n", clientConn.RemoteAddr())

		client := &client{
			conn:   clientConn,
			read:   make(chan []byte),
			write:  make(chan []byte),
			exit:   make(chan error),
			reConn: make(chan bool),
		}

		userConnChan := make(chan net.Conn)
		go AcceptUserConn(userListener, userConnChan)

		go HandleClient(client, userConnChan)

		<-client.reConn
		sugarLogger.Info("重新等待新的client连接..")
		//fmt.Println("重新等待新的client连接..")
	}
}

func HandleClient(client *client, userConnChan chan net.Conn) {
	ctx, cancel := context.WithCancel(context.Background())

	go client.Read(ctx)
	go client.Write(ctx)

	user := &user{
		read:  make(chan []byte),
		write: make(chan []byte),
		exit:  make(chan error),
	}

	defer func() {
		_ = client.conn.Close()
		_ = user.conn.Close()
		client.reConn <- true
	}()

	for {
		select {
		case userConn := <-userConnChan:
			user.conn = userConn
			go handle(ctx, client, user)
		case err := <-client.exit:
			sugarLogger.Error("client出现错误, 关闭连接", err.Error())
			//fmt.Println("client出现错误, 关闭连接", err.Error())
			cancel()
			return
		case err := <-user.exit:
			sugarLogger.Error("user出现错误，关闭连接", err.Error())
			//fmt.Println("user出现错误，关闭连接", err.Error())
			cancel()
			return
		}
	}
}

// 将两个Socket通道链接
// 1. 将从user收到的信息发给client
// 2. 将从client收到信息发给user
func handle(ctx context.Context, client *client, user *user) {
	go user.Read(ctx)
	go user.Write(ctx)

	for {
		select {
		case userRecv := <-user.read:
			// 收到从user发来的信息
			client.write <- userRecv
		case clientRecv := <-client.read:
			// 收到从client发来的信息
			user.write <- clientRecv

		case <-ctx.Done():
			return
		}
	}
}

// 等待user连接
func AcceptUserConn(userListener net.Listener, connChan chan net.Conn) {
	userConn, err := userListener.Accept()
	if err != nil {
		panic(err)
	}
	sugarLogger.Info("user connect: %s \n", userConn.RemoteAddr())
	//fmt.Printf("user connect: %s \n", userConn.RemoteAddr())
	connChan <- userConn
}
